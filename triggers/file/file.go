package file

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// allEvents is the default set of event strings when none are specified.
var allEvents = map[string]bool{
	"create": true,
	"write":  true,
	"delete": true,
	"rename": true,
	"chmod":  true,
}

// FileTrigger fires its action expression in response to filesystem events.
// It implements Startable, PostStartable, Stoppable, and Gettable.
type FileTrigger struct {
	name            string
	config          *cfg.Config
	watchPath       string
	actionExpr      hcl.Expression
	skipWhenExpr    hcl.Expression // nil if not provided
	events          map[string]bool
	recursive       bool
	filter          string        // glob pattern, empty = unset
	debounce        time.Duration // 0 = disabled
	onStartExisting bool
	disabled        bool

	// runtime
	watcher   *fsnotify.Watcher
	mu        sync.Mutex
	debounceT map[string]*time.Timer // path → pending timer (guarded by mu)
	debounceE map[string]string      // path → latest event string (guarded by mu)

	runCount   atomic.Int64
	lastMu     sync.RWMutex
	lastResult cty.Value
	lastError  error

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// Get returns the most recently completed action result, null before the first
// run, or an error if the most recent evaluation failed. Implements Gettable.
func (t *FileTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	t.lastMu.RLock()
	defer t.lastMu.RUnlock()
	if t.lastError != nil {
		return cty.NilVal, t.lastError
	}
	if t.lastResult == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return t.lastResult, nil
}

// Start validates the watch path, creates the fsnotify watcher, registers all
// paths, and launches the event-loop goroutine. Implements Startable.
func (t *FileTrigger) Start() error {
	if t.disabled {
		return nil
	}

	info, err := os.Stat(t.watchPath)
	if err != nil {
		return fmt.Errorf("file trigger %q: path %q does not exist: %w", t.name, t.watchPath, err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("file trigger %q: failed to create watcher: %w", t.name, err)
	}
	t.watcher = w

	if err := t.watcher.Add(t.watchPath); err != nil {
		_ = t.watcher.Close()
		return fmt.Errorf("file trigger %q: failed to watch %q: %w", t.name, t.watchPath, err)
	}

	if t.recursive && info.IsDir() {
		if err := t.addSubdirs(t.watchPath); err != nil {
			_ = t.watcher.Close()
			return fmt.Errorf("file trigger %q: recursive watch setup failed: %w", t.name, err)
		}
	}

	t.stopCh = make(chan struct{})
	t.wg.Add(1)
	go t.eventLoop()

	return nil
}

// addSubdirs walks path and calls watcher.Add on each subdirectory.
func (t *FileTrigger) addSubdirs(path string) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != path {
			if addErr := t.watcher.Add(p); addErr != nil {
				t.config.Logger.Warn("file trigger: failed to watch subdirectory",
					zap.String("name", t.name), zap.String("path", p), zap.Error(addErr))
			}
		}
		return nil
	})
}

// PostStart dispatches synthetic "create" events for existing files when
// on_start_existing is set. The live watch is already established by Start(),
// so real OS events are not lost during enumeration. Implements PostStartable.
func (t *FileTrigger) PostStart() error {
	if t.disabled || !t.onStartExisting {
		return nil
	}

	err := filepath.WalkDir(t.watchPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			t.maybeDispatch(p, "create")
		}
		return nil
	})
	if err != nil {
		t.config.Logger.Error("file trigger: on_start_existing walk failed",
			zap.String("name", t.name), zap.Error(err))
	}
	return nil
}

// Stop signals the event loop to exit, closes the watcher, and waits for all
// in-flight action goroutines to finish. Implements Stoppable.
func (t *FileTrigger) Stop() error {
	if t.stopCh == nil {
		return nil
	}
	close(t.stopCh)
	if t.watcher != nil {
		_ = t.watcher.Close()
	}
	t.wg.Wait()
	return nil
}

// eventLoop reads from the fsnotify channels until stopped.
func (t *FileTrigger) eventLoop() {
	defer t.wg.Done()
	for {
		select {
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			t.handleFsEvent(event)
		case err, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			t.config.Logger.Error("file trigger: watcher error",
				zap.String("name", t.name), zap.Error(err))
		case <-t.stopCh:
			return
		}
	}
}

// opToEvent maps a single fsnotify Op bit to a VCL event string.
var opToEvent = []struct {
	op    fsnotify.Op
	event string
}{
	{fsnotify.Create, "create"},
	{fsnotify.Write, "write"},
	{fsnotify.Remove, "delete"},
	{fsnotify.Rename, "rename"},
	{fsnotify.Chmod, "chmod"},
}

// handleFsEvent maps the fsnotify event to VCL event strings and dispatches.
func (t *FileTrigger) handleFsEvent(event fsnotify.Event) {
	// If recursive and a directory was created, add it to the watch set.
	if t.recursive && event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if addErr := t.watcher.Add(event.Name); addErr != nil {
				t.config.Logger.Warn("file trigger: failed to watch new directory",
					zap.String("name", t.name), zap.String("path", event.Name), zap.Error(addErr))
			}
		}
	}

	for _, m := range opToEvent {
		if event.Has(m.op) {
			t.maybeDispatch(event.Name, m.event)
		}
	}
}

// maybeDispatch applies event and filter checks, then either debounces or
// immediately launches a dispatch goroutine.
func (t *FileTrigger) maybeDispatch(path, eventStr string) {
	// Check event type filter.
	if !t.events[eventStr] {
		return
	}

	// Check glob filter.
	if t.filter != "" {
		matched, err := filepath.Match(t.filter, filepath.Base(path))
		if err != nil || !matched {
			// Also try matching against the full path for patterns with path separators.
			if err != nil {
				return
			}
			fullMatched, ferr := filepath.Match(t.filter, path)
			if ferr != nil || !fullMatched {
				return
			}
		}
	}

	if t.debounce > 0 {
		t.mu.Lock()
		t.debounceE[path] = eventStr
		if timer, ok := t.debounceT[path]; ok {
			timer.Reset(t.debounce)
		} else {
			capturedPath := path
			t.debounceT[path] = time.AfterFunc(t.debounce, func() {
				t.mu.Lock()
				ev := t.debounceE[capturedPath]
				delete(t.debounceT, capturedPath)
				delete(t.debounceE, capturedPath)
				t.mu.Unlock()
				t.wg.Add(1)
				go func() {
					defer t.wg.Done()
					t.dispatch(capturedPath, ev)
				}()
			})
		}
		t.mu.Unlock()
		return
	}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.dispatch(path, eventStr)
	}()
}

// dispatch builds the eval context and evaluates action (and skip_when) for
// a single filesystem event.
func (t *FileTrigger) dispatch(eventPath, eventStr string) {
	runCount := t.runCount.Load()

	t.lastMu.RLock()
	lastResult := t.lastResult
	lastErr := t.lastError
	t.lastMu.RUnlock()

	lastResultVal := lastResult
	if lastResultVal == cty.NilVal {
		lastResultVal = cty.NullVal(cty.DynamicPseudoType)
	}
	lastErrStr := cty.NullVal(cty.String)
	if lastErr != nil {
		lastErrStr = cty.StringVal(lastErr.Error())
	}

	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "file").
		WithStringAttribute("name", t.name).
		WithStringAttribute("path", t.watchPath).
		WithStringAttribute("event_path", eventPath).
		WithStringAttribute("event", eventStr).
		WithInt64Attribute("run_count", runCount).
		WithAttribute("last_result", lastResultVal).
		WithAttribute("last_error", lastErrStr).
		BuildEvalContext(t.config.EvalCtx())
	if err != nil {
		t.config.Logger.Error("file trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		return
	}

	if t.skipWhenExpr != nil {
		skipVal, diags := t.skipWhenExpr.Value(evalCtx)
		if diags.HasErrors() {
			t.config.Logger.Error("file trigger: skip_when error",
				zap.String("name", t.name), zap.Error(diags))
			return
		}
		if skipVal.Type() == cty.Bool && skipVal.True() {
			return
		}
	}

	t.config.Logger.Debug("file trigger: executing action",
		zap.String("name", t.name), zap.String("event", eventStr), zap.String("event_path", eventPath))

	actionVal, actionDiags := t.actionExpr.Value(evalCtx)
	var actionErr error
	if actionDiags.HasErrors() {
		actionErr = actionDiags
		actionVal = cty.NilVal
		t.config.Logger.Error("file trigger: action error",
			zap.String("name", t.name), zap.String("event_path", eventPath), zap.Error(actionErr))
	} else {
		t.config.Logger.Debug("file trigger: action completed",
			zap.String("name", t.name), zap.String("event_path", eventPath))
	}

	t.runCount.Add(1)

	t.lastMu.Lock()
	t.lastResult = actionVal
	t.lastError = actionErr
	t.lastMu.Unlock()
}

// --- Capsule type ---

var FileTriggerCapsuleType = cty.CapsuleWithOps("file_trigger", reflect.TypeOf((*FileTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("file_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "FileTrigger"
	},
})

func NewFileTriggerCapsule(t *FileTrigger) cty.Value {
	return cty.CapsuleVal(FileTriggerCapsuleType, t)
}

func GetFileTriggerFromCapsule(val cty.Value) (*FileTrigger, error) {
	if val.Type() != FileTriggerCapsuleType {
		return nil, fmt.Errorf("expected file_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*FileTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a FileTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// Ensure FileTrigger implements types.Gettable at compile time.
var _ types.Gettable = (*FileTrigger)(nil)

// --- Block processing ---

var validEvents = map[string]bool{
	"create": true,
	"write":  true,
	"delete": true,
	"rename": true,
	"chmod":  true,
}

type triggerFileBody struct {
	Path            hcl.Expression `hcl:"path"`
	Action          hcl.Expression `hcl:"action"`
	Events          hcl.Expression `hcl:"events,optional"`
	Recursive       *bool          `hcl:"recursive,optional"`
	Filter          hcl.Expression `hcl:"filter,optional"`
	Debounce        hcl.Expression `hcl:"debounce,optional"`
	OnStartExisting *bool          `hcl:"on_start_existing,optional"`
	SkipWhen        hcl.Expression `hcl:"skip_when,optional"`
	Disabled        *bool          `hcl:"disabled,optional"`
}

func init() {
	cfg.RegisterConditionalTriggerType(func(c *cfg.Config) map[string]cfg.TriggerRegistration {
		if c.BaseDir == "" {
			return nil
		}
		return map[string]cfg.TriggerRegistration{
			"file": {
				Process:         processFileTrigger,
				HasDependencyId: true,
			},
		}
	})
}

func processFileTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerFileBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	// Evaluate path.
	pathVal, pathDiags := body.Path.Value(config.EvalCtx())
	diags = diags.Extend(pathDiags)
	if diags.HasErrors() {
		return diags
	}
	if pathVal.Type() != cty.String {
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid path",
			Detail:   fmt.Sprintf("path must be a string, got %s", pathVal.Type().FriendlyName()),
			Subject:  body.Path.StartRange().Ptr(),
		})
	}
	watchPath := pathVal.AsString()
	if !filepath.IsAbs(watchPath) {
		watchPath = filepath.Join(config.BaseDir, watchPath)
	}

	// Evaluate and validate events list.
	eventsSet := maps(allEvents) // default: all events
	if cfg.IsExpressionProvided(body.Events) {
		eventsVal, eventsDiags := body.Events.Value(config.EvalCtx())
		diags = diags.Extend(eventsDiags)
		if diags.HasErrors() {
			return diags
		}
		if !eventsVal.Type().IsListType() && !eventsVal.Type().IsTupleType() {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid events value",
				Detail:   "events must be a list of strings",
				Subject:  body.Events.StartRange().Ptr(),
			})
		}
		eventsSet = make(map[string]bool)
		for it := eventsVal.ElementIterator(); it.Next(); {
			_, v := it.Element()
			if v.Type() != cty.String {
				return append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid event type",
					Detail:   "each event must be a string",
					Subject:  body.Events.StartRange().Ptr(),
				})
			}
			ev := v.AsString()
			if !validEvents[ev] {
				return append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Unknown event type",
					Detail:   fmt.Sprintf("unknown event type %q; valid values are: create, write, delete, rename, chmod", ev),
					Subject:  body.Events.StartRange().Ptr(),
				})
			}
			eventsSet[ev] = true
		}
	}

	// Evaluate filter.
	filter := ""
	if cfg.IsExpressionProvided(body.Filter) {
		filterVal, filterDiags := body.Filter.Value(config.EvalCtx())
		diags = diags.Extend(filterDiags)
		if diags.HasErrors() {
			return diags
		}
		if filterVal.Type() != cty.String {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid filter",
				Detail:   fmt.Sprintf("filter must be a string, got %s", filterVal.Type().FriendlyName()),
				Subject:  body.Filter.StartRange().Ptr(),
			})
		}
		filter = filterVal.AsString()
		// Validate glob syntax early.
		if _, err := filepath.Match(filter, ""); err != nil {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid filter pattern",
				Detail:   fmt.Sprintf("filter %q is not a valid glob pattern: %v", filter, err),
				Subject:  body.Filter.StartRange().Ptr(),
			})
		}
	}

	// Evaluate debounce.
	var debounce time.Duration
	if cfg.IsExpressionProvided(body.Debounce) {
		var debounceDiags hcl.Diagnostics
		debounce, debounceDiags = config.ParseDuration(body.Debounce)
		diags = diags.Extend(debounceDiags)
		if diags.HasErrors() {
			return diags
		}
	}

	// Optional booleans.
	recursive := false
	if body.Recursive != nil {
		recursive = *body.Recursive
	}
	onStartExisting := false
	if body.OnStartExisting != nil {
		onStartExisting = *body.OnStartExisting
	}
	disabled := false
	if body.Disabled != nil {
		disabled = *body.Disabled
	}

	// skip_when expression.
	var skipWhenExpr hcl.Expression
	if cfg.IsExpressionProvided(body.SkipWhen) {
		skipWhenExpr = body.SkipWhen
	}

	name := block.Labels[1]
	t := &FileTrigger{
		name:            name,
		config:          config,
		watchPath:       watchPath,
		actionExpr:      body.Action,
		skipWhenExpr:    skipWhenExpr,
		events:          eventsSet,
		recursive:       recursive,
		filter:          filter,
		debounce:        debounce,
		onStartExisting: onStartExisting,
		disabled:        disabled,
		debounceT:       make(map[string]*time.Timer),
		debounceE:       make(map[string]string),
	}

	config.CtyTriggerMap[name] = NewFileTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.PostStartables = append(config.PostStartables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}

// maps returns a shallow copy of the given map.
func maps[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
