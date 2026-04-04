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
)

type ConfigBuilder struct {
	logger        *zap.Logger
	sources       []any
	features      map[string]string
	blockHandlers map[string]BlockHandler
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
	Logger    *zap.Logger
	Functions map[string]function.Function
	Constants map[string]cty.Value
	evalCtx   *hcl.EvalContext
	Features  map[string]string
	BaseDir   string
	WriteDir  string

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

	MetricsServers map[string]MetricsRegistrar
	OtlpClients    map[string]OtlpClient
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
	config := &Config{
		Logger:           cb.logger,
		Features:         cb.features,
		BaseDir:          cb.features["readfiles"],
		WriteDir:         cb.features["writefiles"],
		Constants:        make(map[string]cty.Value),
		SigActions:       NewSignalActionHandler(cb.logger),
		Buses:            make(map[string]bus.EventBus),
		CtyBusMap:        make(map[string]cty.Value),
		Clients:          make(map[string]map[string]Client),
		CtyClientMap:     make(map[string]cty.Value),
		Servers:          make(map[string]map[string]Listener),
		CtyServerMap:     make(map[string]cty.Value),
		CtyTriggerMap:    make(map[string]cty.Value),
		CtyVarMap:        make(map[string]cty.Value),
		TriggerDefRanges: make(map[string]hcl.Range),
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

	bodies, diags := ParseConfigFiles(cb.sources...)
	if diags.HasErrors() {
		return nil, diags
	}

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

	config.Functions, addDiags = config.GetFunctions(functions)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Update evaluation context with functions
	config.evalCtx.Functions = config.Functions

	blocks, addDiags := cb.GetBlocks(nonEditorBodies)
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
