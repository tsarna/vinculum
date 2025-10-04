package config

import (
	"fmt"

	"github.com/hashicorp/go-cty-funcs/filesystem"
	"github.com/hashicorp/hcl/v2"
	"github.com/robfig/cron/v3"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/functions"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

type ConfigBuilder struct {
	logger        *zap.Logger
	sources       []any
	baseDir       string
	blockHandlers map[string]BlockHandler
}

type Startable interface {
	Start() error
}

type Config struct {
	Logger    *zap.Logger
	Functions map[string]function.Function
	Constants map[string]cty.Value
	evalCtx   *hcl.EvalContext
	BaseDir   string

	SigActions     *SignalActionHandler
	Startables     []Startable
	BusCapsuleType cty.Type
	CtyBusMap      map[string]cty.Value
	Buses          map[string]bus.EventBus
	CtyServerMap   map[string]cty.Value
	Servers        map[string]map[string]Listener

	Crons map[string]*cron.Cron
}

func NewConfig() *ConfigBuilder {
	return &ConfigBuilder{
		sources:       make([]any, 0),
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

func (c *ConfigBuilder) WithBaseDir(baseDir string) *ConfigBuilder {
	c.baseDir = baseDir
	return c
}

func (cb *ConfigBuilder) Build() (*Config, hcl.Diagnostics) {
	config := &Config{
		Logger:       cb.logger,
		BaseDir:      cb.baseDir,
		Constants:    make(map[string]cty.Value),
		SigActions:   NewSignalActionHandler(cb.logger),
		Buses:        make(map[string]bus.EventBus),
		CtyBusMap:    make(map[string]cty.Value),
		Servers:      make(map[string]map[string]Listener),
		CtyServerMap: make(map[string]cty.Value),
		Crons:        make(map[string]*cron.Cron),
	}

	bodies, diags := ParseConfigFiles(cb.sources...)
	if diags.HasErrors() {
		return nil, diags
	}

	// Add environment variables to the evaluation context
	config.Constants["env"] = GetEnvObject()
	config.Constants["httpstatus"] = getStatusCodeObject()

	// Create evaluation context that will be updated as we process
	config.evalCtx = &hcl.EvalContext{
		Variables: config.Constants,
	}

	functions, nonFunctionBodies, addDiags := config.ExtractUserFunctions(bodies)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	config.Functions, addDiags = config.GetFunctions(functions)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return nil, diags
	}

	// Update evaluation context with functions
	config.evalCtx.Functions = config.Functions

	blocks, addDiags := cb.GetBlocks(nonFunctionBodies)
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

	blocks, sortDiags := cb.SortBlocksByDependencies(blocks)
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

// ExtractUserFunctions wraps the functions package ExtractUserFunctions
func (c *Config) ExtractUserFunctions(bodies []hcl.Body) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	return functions.ExtractUserFunctions(bodies, c.evalCtx)
}

// GetFunctions wraps the functions package and adds config-specific functions
func (c *Config) GetFunctions(userFuncs map[string]function.Function) (map[string]function.Function, hcl.Diagnostics) {
	funcs := functions.GetStandardLibraryFunctions()
	diags := hcl.Diagnostics{}

	for name, function := range functions.GetLogFunctions(c.Logger) {
		funcs[name] = function
	}

	funcs["diff"] = functions.DiffFunc
	funcs["error"] = functions.ErrorFunc
	funcs["patch"] = functions.PatchFunc
	funcs["send"] = SendFunction(c)
	funcs["sendjson"] = SendJSONFunction(c)
	funcs["sendgo"] = SendGoFunction(c)
	funcs["typeof"] = functions.TypeOfFunc

	if c.BaseDir != "" {
		funcs["abspath"] = filesystem.AbsPathFunc
		funcs["basename"] = filesystem.BasenameFunc
		funcs["dirname"] = filesystem.DirnameFunc
		funcs["file"] = filesystem.MakeFileFunc(c.BaseDir, false)
		funcs["fileexists"] = filesystem.MakeFileExistsFunc(c.BaseDir)
		funcs["fileset"] = filesystem.MakeFileSetFunc(c.BaseDir)
		funcs["file"] = filesystem.MakeFileFunc(c.BaseDir, true)
		funcs["filebase64"] = filesystem.MakeFileFunc(c.BaseDir, true)
		funcs["pathexpand"] = filesystem.PathExpandFunc
	}

	for name, function := range userFuncs {
		if _, exists := funcs[name]; exists {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate function",
				Detail:   fmt.Sprintf("Function %s is reserved and can't be overridden", name),
			})
			continue
		}
		funcs[name] = function
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
