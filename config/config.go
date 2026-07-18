package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ConfigBuilder struct {
	logger        *zap.Logger
	sources       []any
	features      map[string]string
	blockHandlers map[string]BlockHandler
	pluginPath    string
}

type Startable interface {
	Start() error
}

// Stoppable is implemented by components that need graceful shutdown.
// Stop is called in reverse-start order on SIGINT/SIGTERM.
type Stoppable interface {
	Stop() error
}

// PostStartable is implemented by components that need to run logic after all
// Startable components have completed their Start() calls. PostStart() is called
// once, in the same order as Start(), after the full startup sequence completes.
type PostStartable interface {
	PostStart() error
}

// PreStoppable is implemented by components that need to run logic before any
// Stoppable components have their Stop() called. PreStop() is called in reverse
// registration order, before the full teardown sequence begins. This guarantees
// that the full runtime environment (clients, buses, subscriptions) is still
// available when PreStop() executes.
type PreStoppable interface {
	PreStop() error
}

type Config struct {
	Logger *zap.Logger
	// UserLogger is Logger with Go caller and stacktrace suppressed. Use it
	// at sites that report errors caused by user-supplied VCL (expression
	// eval failures, assertion failures, action errors, etc.) where the Go
	// source location and stack are noise rather than signal.
	UserLogger *zap.Logger
	Functions  map[string]function.Function
	Constants  map[string]cty.Value
	evalCtx    *hcl.EvalContext
	Features   map[string]string
	BaseDir    string
	WriteDir   string

	SigActions       *SignalActionHandler
	Startables       []Startable
	PostStartables   []PostStartable
	PreStoppables    []PreStoppable
	Stoppables       []Stoppable
	BusCapsuleType   cty.Type
	CtyBusMap        map[string]cty.Value
	Buses            map[string]bus.EventBus
	CtyClientMap     map[string]cty.Value
	Clients          map[string]map[string]Client
	CtyServerMap     map[string]cty.Value
	Servers          map[string]map[string]Listener
	CtyVarMap        map[string]cty.Value
	CtyTriggerMap    map[string]cty.Value
	TriggerDefRanges map[string]hcl.Range
	CtyConditionMap  map[string]cty.Value
	CtyFsmMap        map[string]cty.Value
	CtyWireFormatMap map[string]cty.Value

	MetricsServers map[string]MetricsRegistrar
	OtlpClients    map[string]OtlpClient

	// transformPluginFuncs holds functions contributed by registered transform
	// plugins, merged into the transform-only eval context. Built once during
	// Build() after the .vinit pass (so plugin-loaded transforms are seen);
	// collisions with built-ins or between plugins are surfaced as diagnostics
	// at that point.
	transformPluginFuncs map[string]function.Function

	// functyState holds the parsed .cty (functy) artifacts: compiled functions
	// merge into the shared function namespace, and top-level var/const
	// declarations (Consts/Vars) are folded into Vinculum's own const/var pools
	// during block preprocessing. Nil when no .cty sources were provided.
	functyState *functyState
}

func NewConfig() *ConfigBuilder {
	return &ConfigBuilder{
		sources:       make([]any, 0),
		features:      make(map[string]string),
		blockHandlers: GetBlockHandlers(),
	}
}

func (c *ConfigBuilder) WithLogger(logger *zap.Logger) *ConfigBuilder {
	c.logger = logger
	return c
}

func (c *ConfigBuilder) WithSources(sources ...any) *ConfigBuilder {
	c.sources = append(c.sources, sources...)
	return c
}

// WithPluginPath sets the directory from which `plugin "<label>" { ... }`
// blocks resolve their .so files. The empty string (default) means plugins
// are not permitted: any `plugin` block encountered in a .vinit file will
// produce a fatal diagnostic.
func (c *ConfigBuilder) WithPluginPath(path string) *ConfigBuilder {
	c.pluginPath = path
	return c
}

// WithFeature enables a named feature flag with the given value.
// Use an empty value to disable a previously set feature.
// Known features: "readfiles" (value = --file-path dir),
//
//	"writefiles" (value = --write-path dir),
//	"allowkill"  (value = "true").
func (c *ConfigBuilder) WithFeature(name, value string) *ConfigBuilder {
	if value == "" {
		delete(c.features, name)
	} else {
		c.features[name] = value
	}
	return c
}

func (cb *ConfigBuilder) Build() (*Config, hcl.Diagnostics) {
	userLogger := cb.logger
	if userLogger != nil {
		userLogger = userLogger.WithOptions(
			zap.WithCaller(false),
			zap.AddStacktrace(zapcore.FatalLevel+1),
		)
	}

	config := &Config{
		Logger:           cb.logger,
		UserLogger:       userLogger,
		Features:         cb.features,
		BaseDir:          cb.features["readfiles"],
		WriteDir:         cb.features["writefiles"],
		Constants:        make(map[string]cty.Value),
		SigActions:       NewSignalActionHandler(cb.logger, userLogger),
		Buses:            make(map[string]bus.EventBus),
		CtyBusMap:        make(map[string]cty.Value),
		Clients:          make(map[string]map[string]Client),
		CtyClientMap:     make(map[string]cty.Value),
		Servers:          make(map[string]map[string]Listener),
		CtyServerMap:     make(map[string]cty.Value),
		CtyTriggerMap:    make(map[string]cty.Value),
		CtyConditionMap:  make(map[string]cty.Value),
		CtyFsmMap:        make(map[string]cty.Value),
		CtyVarMap:        make(map[string]cty.Value),
		TriggerDefRanges: make(map[string]hcl.Range),
		CtyWireFormatMap: make(map[string]cty.Value),
		MetricsServers:   make(map[string]MetricsRegistrar),
	}

	// Validate write-path is under file-path
	if config.WriteDir != "" {
		if config.BaseDir == "" {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid --write-path",
				Detail:   "--write-path requires --file-path to also be set",
			}}
		}
		rel, err := filepath.Rel(filepath.Clean(config.BaseDir), filepath.Clean(config.WriteDir))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid --write-path",
				Detail:   fmt.Sprintf("--write-path %q must be within --file-path %q", config.WriteDir, config.BaseDir),
			}}
		}
	}

	// Pass 1: process .vinit files (plugin loading, etc.) before any .vcl
	// parsing. Plugin-registered ambients, functions, and types are visible
	// to the rest of Build() below.
	if vinitDiags := processVinit(cb.sources, cb.pluginPath, cb.logger); vinitDiags.HasErrors() {
		return nil, vinitDiags
	}

	bodies, diags := ParseConfigFiles(cb.sources...)
	if diags.HasErrors() {
		return nil, diags
	}

	// Build the functy parser (configured with Vinculum's named types) up front,
	// so its type resolver is available to VCL `var` type constraints even when
	// no .cty sources are present. Collect functy (.cty) sources from the same
	// source set; they are parsed and compiled below, alongside the .vcl user
	// functions.
	config.functyState = newFunctyState()
	functySources, functyDiags := collectFunctySources(cb.sources)
	diags = diags.Extend(functyDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Build the merged transform-plugin function map now that plugins have
	// had a chance to register. Collisions with built-in transform names or
	// between two plugins surface as fatal diagnostics here.
	transformPluginFuncs, transformPluginDiags := config.buildTransformPluginFunctions()
	diags = diags.Extend(transformPluginDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	config.transformPluginFuncs = transformPluginFuncs

	// Populate ambient values (env, sys, etc.) from registered providers
	for _, e := range ambientProviders {
		config.Constants[e.name] = e.p(config)
	}

	// Create evaluation context that will be updated as we process
	config.evalCtx = &hcl.EvalContext{
		Variables: config.Constants,
	}

	evalCtxFn := func() *hcl.EvalContext { return config.evalCtx }

	functions, nonFunctionBodies, addDiags := config.ExtractUserFunctions(bodies)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	editorFuncs, nonEditorBodies, addDiags := extractEditorFunctions(nonFunctionBodies, config, evalCtxFn)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	for name, fn := range editorFuncs {
		functions[name] = fn
	}

	procFuncs, nonProcBodies, addDiags := extractProcedureFunctions(nonEditorBodies, config, evalCtxFn)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	for name, fn := range procFuncs {
		functions[name] = fn
	}

	// Compile functy (.cty) functions and merge them into the same user-function
	// map, so GetFunctions collision-checks them against builtins and each other.
	// Compiled functions capture evalCtxFn for late binding, exactly like
	// procedures. The parsed result is retained on config.functyState for later
	// phases (top-level var/const folding).
	functyFuncs, addDiags := config.functyState.compile(functySources, evalCtxFn)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}
	// Share the .cty file map with the signal handler so a functy throw from a
	// signal action renders with source context (it has no *Config reference).
	config.SigActions.FunctyFiles = config.functyFileMap()

	for name, fn := range functyFuncs {
		functions[name] = fn
	}

	config.Functions, addDiags = config.GetFunctions(functions)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Update evaluation context with functions
	config.evalCtx.Functions = config.Functions

	blocks, addDiags := cb.GetBlocks(nonProcBodies)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Preprocess blocks

	blockHandlers := GetBlockHandlers()

	for _, block := range blocks {
		if handler, ok := blockHandlers[block.Type]; ok {
			diags = diags.Extend(handler.Preprocess(block))
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}

	for _, handler := range blockHandlers {
		diags = diags.Extend(handler.FinishPreprocessing(config))
	}
	if diags.HasErrors() {
		return nil, diags
	}

	// Evaluate namespaced functy consts into their per-namespace scopes now that the
	// global const surface (config.Constants) is resolved by the pass above. Global
	// ("") functy consts were folded into that surface; namespaced ones are scoped to
	// their namespace's functy bodies (own+global) and must be in place before any
	// functy function is invoked (var initializers below, then the Process phase).
	diags = diags.Extend(config.functyState.evalNamespacedConsts(config))
	if diags.HasErrors() {
		return nil, diags
	}

	// Evaluate functy top-level var initializers now (consts are resolved by the
	// FinishPreprocessing pass above), so functy var values are in place before
	// the Process phase where consumers (asserts, subscriptions) read var.<name>.
	if vh, ok := blockHandlers["var"].(*VariableBlockHandler); ok {
		diags = diags.Extend(vh.setFunctyVarValues(config))
		if diags.HasErrors() {
			return nil, diags
		}
	}

	// Collect metrics backend block IDs so metric blocks without an explicit
	// server attribute are ordered after them in the dependency sort.
	if mh, ok := blockHandlers["metric"].(*MetricBlockHandler); ok {
		var backendIDs []string
		for _, block := range blocks {
			switch block.Type {
			case "server":
				if len(block.Labels) >= 1 && block.Labels[0] == "metrics" {
					backendIDs = append(backendIDs, "server."+block.Labels[1])
				}
			case "client":
				if len(block.Labels) >= 1 && block.Labels[0] == "otlp" {
					backendIDs = append(backendIDs, "client."+block.Labels[1])
				}
			}
		}
		mh.SetImplicitBackendDeps(backendIDs)
	}

	blocks, sortDiags := cb.SortBlocksByDependencies(blocks, blockHandlers)
	diags = diags.Extend(sortDiags)

	// Process blocks

	for _, block := range blocks {
		if handler, ok := blockHandlers[block.Type]; ok {
			diags = diags.Extend(handler.Process(config, block))
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}

	config.Logger.Info("Config built successfully")

	return config, diags
}

// EvalCtx returns the HCL evaluation context for use by plugin sub-packages.
func (c *Config) EvalCtx() *hcl.EvalContext {
	return c.evalCtx
}

// ExtractUserFunctions wraps the functions package ExtractUserFunctions
func (c *Config) ExtractUserFunctions(bodies []hcl.Body) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	return extractUserFunctions(bodies, c.evalCtx)
}

// GetFunctions builds the function map from all registered function plugins,
// then adds user-defined functions with duplicate detection.
func (c *Config) GetFunctions(userFuncs map[string]function.Function) (map[string]function.Function, hcl.Diagnostics) {
	funcs := make(map[string]function.Function)
	diags := hcl.Diagnostics{}

	for _, p := range functionPlugins {
		for name, fn := range p.getter(c) {
			funcs[name] = fn
		}
	}

	for name, fn := range userFuncs {
		if _, exists := funcs[name]; exists {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate function",
				Detail:   fmt.Sprintf("Function %s is reserved and can't be overridden", name),
			})
			continue
		}
		funcs[name] = fn
	}

	return funcs, diags
}

type errorlessStartable interface {
	Start()
}

func NewErrorlessStartable(startable errorlessStartable) Startable {
	return &ErrorlessStartable{startable: startable}
}

type ErrorlessStartable struct {
	startable errorlessStartable
}

func (e ErrorlessStartable) Start() error {
	e.startable.Start()
	return nil
}
