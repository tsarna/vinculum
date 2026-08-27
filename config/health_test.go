package config

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// fakeReadyable is a contributor whose answer and latency a test controls.
type fakeReadyable struct {
	mu    sync.Mutex
	err   error
	delay time.Duration
	panic bool
	calls atomic.Int32
}

func (f *fakeReadyable) Ready(ctx context.Context) error {
	f.calls.Add(1)

	f.mu.Lock()
	err, delay, shouldPanic := f.err, f.delay, f.panic
	f.mu.Unlock()

	if shouldPanic {
		panic("wedged")
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeReadyable) set(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// bootedHealth returns a Health past its boot gate, which is where every test
// that cares about contributors rather than the gate wants to start.
func bootedHealth() *Health {
	h := NewHealth(zap.NewNop())
	h.SetBooted()
	return h
}

func componentNames(statuses []ComponentStatus) []string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = s.Component
	}
	return names
}

func TestHealthIsReadyWithNoContributors(t *testing.T) {
	h := bootedHealth()

	assert.True(t, h.IsReady(context.Background()))
	assert.Empty(t, h.Failing(context.Background(), ProbeReady, true))
	assert.Equal(t, []string{"process"}, componentNames(h.Status(context.Background(), ProbeReady, true)))
}

func TestHealthBootGateSkipsContributors(t *testing.T) {
	h := NewHealth(zap.NewNop())
	c := &fakeReadyable{}
	h.RegisterReady("client", "mqtt", "broker", c)

	unready := h.Failing(context.Background(), ProbeReady, true)
	require.Len(t, unready, 1)
	assert.Equal(t, "process", unready[0].Component)
	assert.Equal(t, "starting", unready[0].Reason)

	// While starting there is nothing a contributor could say that would
	// change the answer, so none is consulted.
	assert.Zero(t, c.calls.Load())

	h.SetBooted()
	assert.True(t, h.IsReady(context.Background()))
	assert.Equal(t, int32(1), c.calls.Load())
}

func TestHealthDrainGateBypassesTheCache(t *testing.T) {
	h := bootedHealth()
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{})

	require.True(t, h.IsReady(context.Background()))

	// A report was just cached. Drain must be visible anyway: an endpoint
	// controller that learns of it 5s late is 5s of traffic into a pod that is
	// shutting down.
	h.BeginDrain()

	unready := h.Failing(context.Background(), ProbeReady, false)
	require.Len(t, unready, 1)
	assert.Equal(t, "process", unready[0].Component)
	assert.Equal(t, "shutting down", unready[0].Reason)
}

func TestHealthDrainingIsStillLive(t *testing.T) {
	h := bootedHealth()
	h.BeginDrain()

	// A draining process is not wedged. Restarting it mid-drain is the
	// opposite of what shutdown is trying to achieve.
	assert.Empty(t, h.Failing(context.Background(), ProbeLive, true))
}

func TestHealthReportsTheContributorsReason(t *testing.T) {
	h := bootedHealth()
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{
		err: errors.New("not connected: dial tcp 10.0.0.5:1883: connection refused"),
	})
	h.RegisterReady("server", "http", "api", &fakeReadyable{})

	unready := h.Failing(context.Background(), ProbeReady, true)
	require.Len(t, unready, 1)
	assert.Equal(t, "client.broker", unready[0].Component)
	assert.Equal(t, "mqtt", unready[0].Type)
	assert.False(t, unready[0].Ready)
	assert.Equal(t, "not connected: dial tcp 10.0.0.5:1883: connection refused", unready[0].Reason)

	// Every contributor is always evaluated — there is no short-circuit — and
	// the full status list is in registration order behind the process entry.
	all := h.Status(context.Background(), ProbeReady, true)
	assert.Equal(t, []string{"process", "client.broker", "server.api"}, componentNames(all))
}

func TestHealthSeparatesTheTwoProbes(t *testing.T) {
	h := bootedHealth()
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{err: errors.New("not connected")})
	h.RegisterProbe(ProbeLive, "check", "", "pipeline", &fakeReadyable{}, 0)

	assert.Equal(t, []string{"process", "client.broker"},
		componentNames(h.Status(context.Background(), ProbeReady, true)))
	assert.Equal(t, []string{"process", "check.pipeline"},
		componentNames(h.Status(context.Background(), ProbeLive, true)))

	// A broker outage must not restart the pod.
	assert.Empty(t, h.Failing(context.Background(), ProbeLive, true))
}

func TestHealthTimesOutASlowContributor(t *testing.T) {
	h := bootedHealth()
	h.RegisterProbe(ProbeReady, "check", "", "slow", &fakeReadyable{delay: time.Second}, 20*time.Millisecond)
	h.RegisterReady("server", "http", "api", &fakeReadyable{})

	start := time.Now()
	unready := h.Failing(context.Background(), ProbeReady, true)
	elapsed := time.Since(start)

	require.Len(t, unready, 1)
	assert.Equal(t, "check.slow", unready[0].Component)
	assert.Equal(t, "timed out after 20ms", unready[0].Reason)

	// Contributors run concurrently, so one slow check does not serialize the
	// rest behind it.
	assert.Less(t, elapsed, 500*time.Millisecond)
}

func TestHealthRecoversAPanickingContributor(t *testing.T) {
	h := bootedHealth()
	h.RegisterReady("client", "sql", "db", &fakeReadyable{panic: true})
	h.RegisterReady("server", "http", "api", &fakeReadyable{})

	unready := h.Failing(context.Background(), ProbeReady, true)
	require.Len(t, unready, 1)
	assert.Equal(t, "client.db", unready[0].Component)
	assert.Contains(t, unready[0].Reason, "wedged")

	// The healthy contributor was still reported: a wedged component must not
	// take down the probe that would have reported it.
	assert.Equal(t, []string{"process", "client.db", "server.api"},
		componentNames(h.Status(context.Background(), ProbeReady, false)))
}

func TestHealthServesTheCacheUntilRefreshed(t *testing.T) {
	h := bootedHealth()
	c := &fakeReadyable{}
	h.RegisterReady("client", "mqtt", "broker", c)

	require.True(t, h.IsReady(context.Background()))
	require.Equal(t, int32(1), c.calls.Load())

	// A read younger than the TTL costs nothing, and does not notice a change.
	c.set(errors.New("not connected"))
	assert.True(t, h.IsReady(context.Background()))
	assert.Equal(t, int32(1), c.calls.Load())

	// refresh() is the configuration instructing itself, so it is not rate
	// limited — a trigger that says delay = "1s" must not silently get 5s.
	unready := h.Failing(context.Background(), ProbeReady, true)
	assert.Equal(t, int32(2), c.calls.Load())
	require.Len(t, unready, 1)
	assert.Equal(t, "client.broker", unready[0].Component)
}

func TestHealthSingleFlightsConcurrentReaders(t *testing.T) {
	h := bootedHealth()
	c := &fakeReadyable{delay: 50 * time.Millisecond}
	h.RegisterReady("client", "mqtt", "broker", c)

	const readers = 8
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.True(t, h.IsReady(context.Background()))
		}()
	}
	wg.Wait()

	// Two probes and a verbose browser arriving together cost one evaluation.
	assert.Equal(t, int32(1), c.calls.Load())
}

func TestHealthSharedRefreshSurvivesACallerLeaving(t *testing.T) {
	h := bootedHealth()
	c := &fakeReadyable{delay: 100 * time.Millisecond}
	h.RegisterReady("client", "mqtt", "broker", c)

	// The first caller starts the evaluation, then gives up.
	leaving, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		h.Failing(leaving, ProbeReady, true)
	}()
	<-started
	time.Sleep(20 * time.Millisecond)

	// A second caller is waiting on that shared evaluation.
	stayingResult := make(chan []ComponentStatus, 1)
	go func() {
		stayingResult <- h.Failing(context.Background(), ProbeReady, false)
	}()
	time.Sleep(20 * time.Millisecond)

	cancel()
	<-done

	// The caller that left gets nothing; the one still there gets a real
	// answer rather than a spurious failure inherited from someone else's
	// cancelled context.
	select {
	case unready := <-stayingResult:
		assert.Empty(t, unready)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never received the shared evaluation")
	}
	assert.Equal(t, int32(1), c.calls.Load())
}

// countingWatcher records every OnChange it receives.
type countingWatcher struct {
	mu      sync.Mutex
	changes []cty.Value
}

func (w *countingWatcher) OnChange(_ context.Context, _ richcty.Watchable, _, newValue cty.Value) {
	w.mu.Lock()
	w.changes = append(w.changes, newValue)
	w.mu.Unlock()
}

func (w *countingWatcher) values() []cty.Value {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]cty.Value(nil), w.changes...)
}

func TestSysReadyNotifiesOnlyOnAFlip(t *testing.T) {
	h := bootedHealth()
	c := &fakeReadyable{}
	h.RegisterReady("client", "mqtt", "broker", c)

	handle, err := readyHandleFromValue(h.ReadyValue())
	require.NoError(t, err)

	w := &countingWatcher{}
	handle.Watch(w)

	// The first observation establishes the baseline rather than synthesizing
	// a rising edge, and repeats of a value already held are noise.
	require.True(t, h.IsReady(context.Background()))
	h.Failing(context.Background(), ProbeReady, true)
	assert.Empty(t, w.values())

	c.set(errors.New("not connected"))
	h.Failing(context.Background(), ProbeReady, true)
	require.Len(t, w.values(), 1)
	assert.True(t, w.values()[0].RawEquals(cty.False))

	// Still down: a watcher woken with the value it already had is noise.
	h.Failing(context.Background(), ProbeReady, true)
	assert.Len(t, w.values(), 1)

	c.set(nil)
	h.Failing(context.Background(), ProbeReady, true)
	require.Len(t, w.values(), 2)
	assert.True(t, w.values()[1].RawEquals(cty.True))
}

func TestSysReadyIsGettableAndWatchable(t *testing.T) {
	h := bootedHealth()
	val := h.ReadyValue()

	require.True(t, val.Type().Equals(ReadyCapsuleType))

	// A reactive expression finds it the same way it finds a var: a capsule
	// whose encapsulated value is a Watchable.
	watchable, err := richcty.WatchableFromCtyValue(val)
	require.NoError(t, err)
	assert.NotNil(t, watchable)

	gettable, ok := val.EncapsulatedValue().(richcty.Gettable)
	require.True(t, ok, "sys.ready must be Gettable so get(sys.ready) works")

	got, err := gettable.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.True))

	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{err: errors.New("not connected")})

	// get() reads through the cache, so the freshly-registered contributor is
	// not seen until the report is recomputed. That sampling is the documented
	// contract, not an accident.
	got, err = gettable.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.True))

	h.Failing(context.Background(), ProbeReady, true)
	got, err = gettable.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.False))
}

func TestStatusesToCtyProjectsTheReportShape(t *testing.T) {
	broke := time.Now().Add(-4 * time.Minute)
	val := StatusesToCty([]ComponentStatus{
		{Component: "process", Ready: true},
		{Component: "client.broker", Type: "mqtt", Reason: "not connected", Since: broke},
	})

	require.True(t, val.Type().Equals(HealthStatusListType))

	entries := val.AsValueSlice()
	require.Len(t, entries, 2)
	assert.Equal(t, "process", entries[0].GetAttr("component").AsString())
	assert.True(t, entries[0].GetAttr("ready").True())
	assert.Equal(t, "", entries[0].GetAttr("reason").AsString())
	assert.Equal(t, "mqtt", entries[1].GetAttr("type").AsString())
	assert.Equal(t, "not connected", entries[1].GetAttr("reason").AsString())

	// A time capsule, not a string: it is the same type sys.starttime and a
	// trigger "at"'s ctx.scheduled_time carry, so the time:: functions work on
	// it — and it still encodes to RFC 3339 under jsonencode.
	since, err := timecty.GetTime(entries[1].GetAttr("since"))
	require.NoError(t, err)
	assert.True(t, since.Equal(broke))

	assert.True(t, StatusesToCty(nil).Type().Equals(HealthStatusListType),
		"an empty report must still carry the list type, so length() works on it")
}

// withReadinessType marks a type as reporting readiness for the duration of one
// test, so these can exercise applyReadiness without registering a throwaway
// client type into the process-global registry that drives `vinculum schema`.
func withReadinessType(t *testing.T, blockType, typeName string) {
	t.Helper()
	if readinessTypes[blockType] == nil {
		readinessTypes[blockType] = map[string]bool{}
	}
	readinessTypes[blockType][typeName] = true
	t.Cleanup(func() { delete(readinessTypes[blockType], typeName) })
}

func testConfig() *Config {
	c := &Config{Health: NewHealth(zap.NewNop())}
	c.Health.SetBooted()
	return c
}

func TestReadinessIsRegisteredForAParticipatingType(t *testing.T) {
	withReadinessType(t, "client", "mqtt")
	config := testConfig()

	diags := applyReadiness(config, "client", "mqtt", "broker",
		&fakeReadyable{}, nil, hcl.Range{}, hcl.Range{})

	assert.False(t, diags.HasErrors(), "%v", diags)
	assert.Equal(t, []string{"client.broker"}, config.Health.RegisteredComponents())
}

func TestReadinessFalseDeclinesToContribute(t *testing.T) {
	withReadinessType(t, "client", "mqtt")
	config := testConfig()

	no := false
	optional := &fakeReadyable{err: errors.New("not connected")}
	diags := applyReadiness(config, "client", "mqtt", "optional_feed",
		optional, &no, hcl.Range{}, hcl.Range{})

	// A declined component is absent from the report entirely rather than
	// present-and-ignored, so a config with one optional integration is not
	// forced to choose between a permanently unready pod and deleting it.
	assert.False(t, diags.HasErrors(), "%v", diags)
	assert.Empty(t, config.Health.RegisteredComponents())

	config.Health.Failing(context.Background(), ProbeReady, true)
	assert.Zero(t, optional.calls.Load())
}

func TestReadinessIsRejectedOnATypeThatDoesNotReportIt(t *testing.T) {
	config := testConfig()

	// Nothing to report, and nothing written: silently absent.
	diags := applyReadiness(config, "client", "http", "api",
		struct{}{}, nil, hcl.Range{}, hcl.Range{})
	assert.False(t, diags.HasErrors(), "%v", diags)
	assert.Empty(t, config.Health.RegisteredComponents())

	// Nothing to report, but written anyway: rejected rather than accepted and
	// ignored — the same reason it is not documented there.
	yes := true
	attrRange := hcl.Range{Filename: "conf.vcl", Start: hcl.Pos{Line: 3}}
	diags = applyReadiness(config, "client", "http", "api",
		struct{}{}, &yes, attrRange, hcl.Range{Filename: "conf.vcl"})

	require.True(t, diags.HasErrors())
	assert.Equal(t, "Unsupported argument", diags[0].Summary)
	assert.Contains(t, diags[0].Detail, `client type "http" does not report whether it is serving`)
	assert.Equal(t, 3, diags[0].Subject.Start.Line, "the attribute itself should be underlined")
}

func TestReadinessRegistrationAndImplementationMustAgree(t *testing.T) {
	defRange := hcl.Range{Filename: "conf.vcl", Start: hcl.Pos{Line: 7}}

	// Declared, not implemented.
	withReadinessType(t, "client", "paper")
	diags := applyReadiness(testConfig(), "client", "paper", "c",
		struct{}{}, nil, hcl.Range{}, defRange)
	require.True(t, diags.HasErrors())
	assert.Equal(t, "Inconsistent readiness registration", diags[0].Summary)
	assert.Contains(t, diags[0].Detail, "does not implement config.Readyable")

	// Implemented, not declared — the likelier mistake, and the one that would
	// otherwise be silent: the component would contribute to no probe and its
	// `readiness` attribute would go undocumented.
	diags = applyReadiness(testConfig(), "client", "silent", "c",
		&fakeReadyable{}, nil, hcl.Range{}, defRange)
	require.True(t, diags.HasErrors())
	assert.Equal(t, "Inconsistent readiness registration", diags[0].Summary)
	assert.Contains(t, diags[0].Detail, "was not registered with cfg.WithReadiness()")
	assert.Equal(t, 7, diags[0].Subject.Start.Line)
}

func TestReadinessIsDocumentedOnlyWhereItApplies(t *testing.T) {
	common := []*SchemaAttr{{Name: "disabled"}, {Name: readinessAttrName}}

	withReadinessType(t, "client", "mqtt")

	assert.Equal(t, []string{"disabled", "readiness"},
		schemaAttrNames(envelopeAttrsFor("client", "mqtt", common)))
	assert.Equal(t, []string{"disabled"},
		schemaAttrNames(envelopeAttrsFor("client", "http", common)),
		"a type with no readiness to report must not list a setting that does nothing")
}

func schemaAttrNames(attrs []*SchemaAttr) []string {
	names := make([]string, len(attrs))
	for i, a := range attrs {
		names[i] = a.Name
	}
	return names
}

// readyHandleFromValue unwraps the sys.ready capsule.
func readyHandleFromValue(val cty.Value) (*ReadyHandle, error) {
	w, err := richcty.WatchableFromCtyValue(val)
	if err != nil {
		return nil, err
	}
	return w.(*ReadyHandle), nil
}
