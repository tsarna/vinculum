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
)

// Readyable is implemented by components that can report whether they are
// currently able to do their job. It is a probe, not a barrier: Ready returns
// the component's present state promptly and never blocks waiting to become
// ready.
//
// A nil error means ready. A non-nil error means not ready, and its message is
// reported verbatim as the reason, so it should read as a fragment completing
// "<component> is not ready: ...". ctx carries the probe's deadline; an
// implementation that performs I/O must honour it.
type Readyable interface {
	Ready(ctx context.Context) error
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
}

// NewHealth returns a Health in the starting state, with no contributors.
func NewHealth() *Health {
	h := &Health{}
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
	h.mu.Lock()
	defer h.mu.Unlock()
	h.contributors = append(h.contributors, readyContributor{
		probe: probe, kind: kind, typ: typ, name: name, r: r, timeout: timeout,
	})
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
		return []ComponentStatus{gate}
	}

	results := h.evaluate(ctx, force)

	out := make([]ComponentStatus, 0, len(results)+1)
	out = append(out, gate)
	for _, r := range results {
		if r.probe == probe {
			out = append(out, r)
		}
	}
	return out
}

// Unready returns the entries of Status that are not ready. An empty result
// means the probe passes, which is the test callers write:
// `length(health::ready(ctx)) == 0`.
func (h *Health) Unready(ctx context.Context, probe string, force bool) []ComponentStatus {
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
// what get(sys.ready) returns.
func (h *Health) IsReady(ctx context.Context) bool {
	return len(h.Unready(ctx, ProbeReady, false)) == 0
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
// It is a *sampled* watchable, and that is part of its contract rather than an
// implementation note. Every other Watchable in Vinculum — var, condition,
// metric — fires because something actively changed it. This one is refreshed
// only when something asks for readiness, so in a config with no health
// endpoint and no interval trigger it never fires at all. A transition is
// observed up to one asking-interval late, which in the common Kubernetes
// deployment is the probe period.
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
// it. The HTTP JSON rendering serialises the same values, so the two views
// cannot drift.
var HealthStatusType = cty.Object(map[string]cty.Type{
	"component": cty.String,
	"type":      cty.String,
	"ready":     cty.Bool,
	"reason":    cty.String,
})

// HealthStatusListType is what health::ready and health::status return.
var HealthStatusListType = cty.List(HealthStatusType)

// StatusesToCty projects a report into the list-of-objects shape expressions
// see. A list preserves boot order and serialises to JSON the way an operator
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
