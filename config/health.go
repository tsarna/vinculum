package config

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	richcty "github.com/tsarna/rich-cty-types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// Readyable is implemented by components that can report whether they are
// currently able to do their job. It is a probe, not a barrier: Ready returns
// the component's present state promptly and never blocks waiting to become
// ready.
//
// A nil error means ready. A non-nil error means not ready, and its message is
// reported verbatim as the reason, so it should read as a fragment completing
// "<component> is not ready: ...". ctx carries the probe's deadline; an
// implementation that performs I/O must honor it.
type Readyable interface {
	Ready(ctx context.Context) error
}

// ReadyReporter is handed to a contributor that can tell Health when its own
// state changes, rather than waiting to be asked. A nil error means the
// component just became able to do its job; a non-nil one means it just stopped,
// with the same message Ready would have returned.
//
// Reporting is an optimization for promptness, not a second source of truth:
// the component's Ready is still what a probe consults, and must agree. What a
// report buys is that a drop is visible immediately rather than at the next
// refresh, and that the cache does not go on claiming the component is fine.
//
// It must not block. Calling it does no I/O and evaluates no expression, which
// matters because the hooks it is called from are contractually synchronous.
type ReadyReporter func(err error)

// ReadyNotifier is an optional interface a Readyable may implement to be given
// a ReadyReporter when it registers. A component that has a callback for its own
// connect and disconnect — most message clients do — should implement it; one
// that can only answer when polled should not.
type ReadyNotifier interface {
	SetReadyReporter(report ReadyReporter)
}

// The two probes a contributor may belong to. A contributor participates in
// exactly one: readiness answers "should traffic be routed here right now?",
// liveness answers "is this process wedged beyond recovery?", and a signal that
// is treated as both is the classic way to turn a dependency outage into a
// restart loop across every replica.
const (
	ProbeReady = "ready"
	ProbeLive  = "live"
)

// contributorTimeout bounds a single contributor's Ready call. A caller with a
// shorter deadline of its own bounds it further — a probe the kubelet gave up
// on at 1s should not still be pinging a database at 1.9s.
const contributorTimeout = 2 * time.Second

// healthCacheTTL is how long a computed report is served without re-evaluating.
//
// Readiness is computed only when something asks for it; there is no background
// poller, so this is not a rate limit on a ticker but a bound on what an
// unauthenticated caller hammering /readyz can make the process do. With
// Kubernetes' default 10s probe period it also halves the cost of an expensive
// check.
const healthCacheTTL = 5 * time.Second

// processState is the one-way lifecycle of the built-in `process` contributor.
//
// There is no config reload: Health is built once, its contributor set is fixed
// for the life of the process, and this never runs backwards.
type processState int

const (
	processStarting processState = iota
	processServing
	processDraining
)

// ComponentStatus is one contributor's result within a report.
type ComponentStatus struct {
	// Component is the contributor's identity: "client.broker",
	// "check.database", or the bare "process".
	Component string
	// Type is the block's type label ("mqtt", "http"), empty where the kind
	// has none.
	Type string
	// Ready reports whether the contributor is serving.
	Ready bool
	// Reason completes "<component> is not ready: ...", and is empty when
	// Ready is true.
	Reason string

	// probe is which probe this entry belongs to. One evaluation covers both,
	// so a read filters on it; it is not projected to expressions, where the
	// caller already chose a probe by which function it called.
	probe string
}

// readyContributor is one registered participant in a probe.
type readyContributor struct {
	probe string
	kind  string
	typ   string
	name  string
	r     Readyable
	// timeout overrides contributorTimeout for this one contributor. Zero
	// means the default; a `check` block's `timeout` attribute sets it.
	timeout time.Duration
}

// component renders the contributor's identity as a report entry names it.
func (c readyContributor) component() string {
	if c.kind == "" {
		return c.name
	}
	return c.kind + "." + c.name
}

// healthCall is one in-flight evaluation, shared by every caller that arrives
// while it runs.
type healthCall struct {
	done    chan struct{}
	results []ComponentStatus
}

// Health aggregates readiness and liveness for the process.
//
// It runs no goroutine of any kind. A report is computed when something asks
// for one — an HTTP probe, a health:: function, a get(sys.ready), a metrics
// scrape — and never on a timer of its own.
type Health struct {
	mu           sync.Mutex
	state        processState
	contributors []readyContributor

	// results is the last computed per-contributor outcome and when it was
	// computed, shared by every probe and filtered per read.
	results    []ComponentStatus
	computedAt time.Time
	inflight   *healthCall

	// ready is the sys.ready watchable. It holds the last observed aggregate
	// and fires watchers only when that boolean flips.
	ready *ReadyHandle

	// logger reports transitions. It is the UserLogger, since what makes a
	// probe fail is nearly always the configuration or its environment.
	logger *zap.Logger

	// observed is the last verdict logged for each probe, so a transition can
	// be told from a repeat. Absent until the first evaluation, which
	// establishes the baseline rather than announcing an edge.
	observed map[string]bool
}

// NewHealth returns a Health in the starting state, with no contributors.
// logger may be nil, which turns transition logging off.
func NewHealth(logger *zap.Logger) *Health {
	h := &Health{logger: logger, observed: map[string]bool{}}
	h.ready = &ReadyHandle{health: h}
	return h
}

// RegisterReady adds a readiness contributor.
//
// kind is "server", "client", "check", or a plugin-supplied noun; typ is the
// block's type label ("mqtt", "http"), empty for kinds that have none; name is
// the block name. Registration order is boot order, which is the order a report
// lists its entries in: infrastructure first, dependents after.
func (h *Health) RegisterReady(kind, typ, name string, r Readyable) {
	h.register(ProbeReady, kind, typ, name, r, 0)
}

// RegisterProbe adds a contributor to a named probe — ProbeReady or ProbeLive
// — with an optional per-contributor timeout (zero for the default). A
// contributor participates in exactly one probe.
func (h *Health) RegisterProbe(probe, kind, typ, name string, r Readyable, timeout time.Duration) {
	h.register(probe, kind, typ, name, r, timeout)
}

func (h *Health) register(probe, kind, typ, name string, r Readyable, timeout time.Duration) {
	if h == nil || r == nil {
		return
	}
	c := readyContributor{
		probe: probe, kind: kind, typ: typ, name: name, r: r, timeout: timeout,
	}

	h.mu.Lock()
	h.contributors = append(h.contributors, c)
	h.mu.Unlock()

	// A component that can push is handed a reporter bound to its own identity,
	// rather than being asked to name itself on every call. The identity is the
	// one computed here, so it cannot drift from the one the report will appear
	// under.
	if n, ok := r.(ReadyNotifier); ok {
		n.SetReadyReporter(func(err error) { h.report(c, err) })
	}
}

// report records a state change a contributor observed in itself.
//
// Becoming unready short-circuits: one failing contributor decides the
// aggregate, so nothing else is consulted and the notification goes out at once.
// Recovery cannot be concluded the same way — other contributors may still be
// failing — so it only drops the cache, and the next read evaluates for real.
// That is still an improvement on waiting: without it, a probe arriving inside
// the TTL would go on serving the stale failure.
func (h *Health) report(c readyContributor, err error) {
	if h == nil {
		return
	}
	ctx := context.Background()

	h.mu.Lock()
	if err == nil {
		// Drop the cache rather than mark this entry passing: the aggregate is
		// not this component's to decide, and the others may be stale.
		h.results, h.computedAt = nil, time.Time{}
		h.mu.Unlock()
		return
	}

	// Correct the cached entry so a read inside the TTL stops claiming this
	// component is fine. Absent a cached report there is nothing to correct —
	// the next read evaluates and finds the same failure from Ready anyway.
	for i := range h.results {
		if h.results[i].Component == c.component() {
			h.results[i].Ready = false
			h.results[i].Reason = err.Error()
		}
	}

	previous, known := h.observed[c.probe]
	h.observed[c.probe] = false
	h.mu.Unlock()

	if c.probe == ProbeReady {
		h.ready.observe(ctx, false)
	}

	// Named with the component that just dropped. Others may be failing too, but
	// this is the one that changed, and finding out what else is wrong is what
	// the probe endpoint is for.
	if known && previous {
		h.logHealthTransition(c.probe, false, []ComponentStatus{{
			Component: c.component(),
			Type:      c.typ,
			Reason:    err.Error(),
		}})
	}
}

// SetBooted marks boot complete: every Start() and PostStart() has returned.
// Until it is called, readiness is false with reason "starting", so a
// startupProbe pointed at /readyz behaves correctly without a second endpoint.
func (h *Health) SetBooted() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.state == processStarting {
		h.state = processServing
	}
	h.mu.Unlock()
}

// BeginDrain marks the start of teardown. Readiness goes false immediately,
// bypassing the cache, so the endpoint controller removes the pod while
// in-flight work finishes.
//
// It must be called before anything is torn down — in particular before the
// listeners are drained — or a probe gets a connection refusal instead of an
// honest 503.
func (h *Health) BeginDrain() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = processDraining
	h.mu.Unlock()
	// The gate bypasses the cache, so nothing needs invalidating; what does
	// need doing is telling anything watching sys.ready that it just flipped.
	h.ready.observe(context.Background(), false)
}

// ReadyValue is the cty value published as sys.ready.
func (h *Health) ReadyValue() cty.Value {
	if h == nil {
		return cty.NullVal(ReadyCapsuleType)
	}
	return cty.CapsuleVal(ReadyCapsuleType, h.ready)
}

// Status returns every contributor to probe, ready ones included, in
// registration order with the built-in `process` entry first.
//
// The result is subject to the cache (healthCacheTTL); pass force to evaluate
// now regardless, which is what health::refresh(ctx) does.
func (h *Health) Status(ctx context.Context, probe string, force bool) []ComponentStatus {
	if h == nil {
		return nil
	}

	// The process gate is checked first and outside the cache: drain must be
	// visible immediately, and while starting or draining there is nothing a
	// contributor could say that would change the answer.
	gate, gated := h.processStatus(probe)
	if gated {
		if probe == ProbeReady {
			h.ready.observe(ctx, gate.Ready)
		}
		out := []ComponentStatus{gate}
		h.noteVerdict(probe, out)
		return out
	}

	results := h.evaluate(ctx, force)

	out := make([]ComponentStatus, 0, len(results)+1)
	out = append(out, gate)
	for _, r := range results {
		if r.probe == probe {
			out = append(out, r)
		}
	}
	h.noteVerdict(probe, out)
	return out
}

// noteVerdict logs a probe whose verdict has changed since it was last read.
//
// Driven from the read rather than from the evaluation because a probe only has
// a verdict once its own contributors have been filtered out of the shared
// report — the two probes reach opposite conclusions from one evaluation.
// Repeats are silent, and the first read establishes a baseline rather than
// announcing an edge.
func (h *Health) noteVerdict(probe string, statuses []ComponentStatus) {
	passing := true
	var failing []ComponentStatus
	for _, s := range statuses {
		if !s.Ready {
			passing = false
			failing = append(failing, s)
		}
	}

	h.mu.Lock()
	previous, known := h.observed[probe]
	h.observed[probe] = passing
	h.mu.Unlock()

	if !known || previous == passing {
		return
	}
	h.logHealthTransition(probe, passing, failing)
}

// Failing returns the entries of Status that are not passing. An empty result
// means the probe passes, and is what makes the aggregate boolean.
func (h *Health) Failing(ctx context.Context, probe string, force bool) []ComponentStatus {
	all := h.Status(ctx, probe, force)
	out := make([]ComponentStatus, 0, len(all))
	for _, s := range all {
		if !s.Ready {
			out = append(out, s)
		}
	}
	return out
}

// IsReady reports the aggregate readiness boolean, subject to the cache. It is
// what get(sys.ready) and health::ready(ctx) return.
func (h *Health) IsReady(ctx context.Context) bool {
	return len(h.Failing(ctx, ProbeReady, false)) == 0
}

// ValidProbe reports whether name is a probe this language knows. The vocabulary
// is closed and owned by the language, so both the `check` block's `probe`
// attribute and the health:: functions' probe selector validate against this.
func ValidProbe(name string) bool {
	return name == ProbeReady || name == ProbeLive
}

// processStatus returns the built-in `process` entry for probe, and whether it
// gates the answer on its own — that is, whether the remaining contributors are
// skipped entirely.
func (h *Health) processStatus(probe string) (ComponentStatus, bool) {
	h.mu.Lock()
	state := h.state
	h.mu.Unlock()

	switch state {
	case processStarting:
		// Neither probe can pass before boot completes. /readyz reporting
		// "not ready: starting" is what makes a startupProbe work.
		return ComponentStatus{Component: "process", Reason: "starting"}, true
	case processDraining:
		if probe == ProbeLive {
			// A draining process is not wedged; killing it mid-drain is the
			// opposite of what shutdown is trying to achieve.
			return ComponentStatus{Component: "process", Ready: true}, false
		}
		return ComponentStatus{Component: "process", Reason: "shutting down"}, true
	default:
		return ComponentStatus{Component: "process", Ready: true}, false
	}
}

// evaluate returns the per-contributor results, from the cache when it is
// younger than the TTL and force is not set.
//
// Concurrent readers during an evaluation wait for the one in flight rather
// than starting their own, and a reader whose own context dies gives up
// without disturbing the others.
func (h *Health) evaluate(ctx context.Context, force bool) []ComponentStatus {
	h.mu.Lock()

	if !force && h.results != nil && time.Since(h.computedAt) < healthCacheTTL {
		results := h.results
		h.mu.Unlock()
		return results
	}

	if call := h.inflight; call != nil {
		h.mu.Unlock()
		select {
		case <-call.done:
			return call.results
		case <-ctx.Done():
			// This caller left. The evaluation carries on for whoever is still
			// waiting; report what we last knew rather than inventing a state.
			return h.lastResults()
		}
	}

	call := &healthCall{done: make(chan struct{})}
	h.inflight = call
	contributors := append([]readyContributor(nil), h.contributors...)
	h.mu.Unlock()

	// The shared evaluation must not inherit any one caller's cancellation:
	// the first probe to hang up would otherwise abort the refresh every other
	// waiter depends on. Values — trace span, baggage, auth — are kept, so a
	// slow /readyz stays diagnosable as "the database check took 1.8s of it".
	evalCtx := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		evalCtx, cancel = context.WithDeadline(evalCtx, deadline)
		defer cancel()
	}

	results := runContributors(evalCtx, contributors)

	h.mu.Lock()
	h.results = results
	h.computedAt = time.Now()
	h.inflight = nil
	h.mu.Unlock()

	call.results = results
	close(call.done)

	h.ready.observe(ctx, allReady(results, ProbeReady))

	return results
}

func (h *Health) lastResults() []ComponentStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.results
}

// runContributors probes every contributor concurrently and returns their
// results in registration order.
//
// There is no short-circuit mode. Stopping at the first failure would make the
// cached value depend on the verbosity of whoever populated it and the reported
// failure depend on scheduling, and would save nothing the cache does not
// already save. Verbosity is a rendering choice, not an evaluation strategy.
func runContributors(ctx context.Context, contributors []readyContributor) []ComponentStatus {
	results := make([]ComponentStatus, len(contributors))
	var wg sync.WaitGroup
	for i, c := range contributors {
		wg.Add(1)
		go func(i int, c readyContributor) {
			defer wg.Done()
			results[i] = probeOne(ctx, c)
		}(i, c)
	}
	wg.Wait()
	return results
}

// probeOne runs one contributor under its own timeout, converting a panic into
// a not-ready report: a wedged component must not take down the probe that
// would have reported it.
func probeOne(ctx context.Context, c readyContributor) (status ComponentStatus) {
	status = ComponentStatus{Component: c.component(), Type: c.typ, probe: c.probe}

	timeout := c.timeout
	if timeout <= 0 {
		timeout = contributorTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			status.Ready = false
			status.Reason = fmt.Sprintf("panicked: %v", r)
		}
	}()

	err := c.r.Ready(probeCtx)
	if err == nil {
		status.Ready = true
		return status
	}
	if probeCtx.Err() == context.DeadlineExceeded {
		status.Reason = fmt.Sprintf("timed out after %s", timeout)
		return status
	}
	status.Reason = err.Error()
	return status
}

// allReady reports whether every contributor to probe is ready. The process
// gate is not among them: the caller reaches this only once the gate has
// already passed.
func allReady(results []ComponentStatus, probe string) bool {
	for _, r := range results {
		if r.probe == probe && !r.Ready {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// sys.ready
// ---------------------------------------------------------------------------

// ReadyHandle is the value behind sys.ready: a Gettable and Watchable holding
// the aggregate readiness boolean.
//
// It is *partly* a sampled watchable, which is part of its contract rather than
// an implementation note.
//
// A component that knows its own state pushes it — see ReadyNotifier — so a
// lost connection fires here at once, with nothing having asked. That covers
// the transition that matters most and the one most likely to be watched.
//
// The rest is still sampled. Recovery is not one component's to declare, so it
// only invalidates the cache and the next asker sees it; and a `check` block is
// an expression nothing evaluates until a probe arrives. In a configuration
// with no health endpoint and no polling trigger, those are observed only when
// something looks.
type ReadyHandle struct {
	richcty.WatchableMixin
	health *Health

	mu    sync.Mutex
	known bool
	value bool
}

// Get implements richcty.Gettable: the aggregate readiness boolean, refreshing
// subject to the TTL.
func (r *ReadyHandle) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if r == nil || r.health == nil {
		return cty.False, nil
	}
	return cty.BoolVal(r.health.IsReady(ctx)), nil
}

// observe records a freshly computed aggregate and notifies watchers if it
// flipped.
//
// Unlike var, whose watchers fire on every set() because a repeated write is a
// meaningful heartbeat, this fires only on a change: it is a derived state, and
// a watcher woken with the value it already had is noise.
func (r *ReadyHandle) observe(ctx context.Context, value bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	old, known := r.value, r.known
	r.value, r.known = value, true
	r.mu.Unlock()

	if known && old == value {
		return
	}
	if !known {
		// The first observation establishes the baseline rather than
		// synthesizing an edge, the same way a condition's Bootstrap does.
		return
	}
	r.NotifyAll(ctx, r, cty.BoolVal(old), cty.BoolVal(value))
}

// ReadyCapsuleType is the capsule behind sys.ready.
var ReadyCapsuleType = cty.CapsuleWithOps("ready", reflect.TypeOf(ReadyHandle{}), &cty.CapsuleOps{
	TypeGoString: func(_ reflect.Type) string { return "config.ReadyCapsuleType" },
	GoString: func(v any) string {
		return "config.ReadyCapsuleType"
	},
	RawEquals: func(a, b any) bool {
		return a.(*ReadyHandle) == b.(*ReadyHandle)
	},
})

// ---------------------------------------------------------------------------
// cty projection
// ---------------------------------------------------------------------------

// HealthStatusType is the object type of one report entry as expressions see
// it. The HTTP JSON rendering serializes the same values, so the two views
// cannot drift.
var HealthStatusType = cty.Object(map[string]cty.Type{
	"component": cty.String,
	"type":      cty.String,
	"ready":     cty.Bool,
	"reason":    cty.String,
})

// HealthStatusListType is what health::status and health::failing return.
var HealthStatusListType = cty.List(HealthStatusType)

// StatusesToCty projects a report into the list-of-objects shape expressions
// see. A list preserves boot order and serializes to JSON the way an operator
// expects, where a map keyed by name would do neither.
func StatusesToCty(statuses []ComponentStatus) cty.Value {
	if len(statuses) == 0 {
		return cty.ListValEmpty(HealthStatusType)
	}
	vals := make([]cty.Value, len(statuses))
	for i, s := range statuses {
		vals[i] = cty.ObjectVal(map[string]cty.Value{
			"component": cty.StringVal(s.Component),
			"type":      cty.StringVal(s.Type),
			"ready":     cty.BoolVal(s.Ready),
			"reason":    cty.StringVal(s.Reason),
		})
	}
	return cty.ListVal(vals)
}

// RegisteredComponents returns the component names of every contributor, sorted.
// Used by tests and diagnostics; not on any probe path.
func (h *Health) RegisteredComponents() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.contributors))
	for _, c := range h.contributors {
		names = append(names, c.component())
	}
	sort.Strings(names)
	return names
}
