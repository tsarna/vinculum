package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-cty-funcs/filesystem"
	"github.com/hashicorp/hcl/v2"
	"github.com/robfig/cron/v3"
	randcty "github.com/tsarna/rand-cty-funcs"
	timecty "github.com/tsarna/time-cty-funcs"
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
	writeDir      string
	blockHandlers map[string]BlockHandler
}

type Startable interface {
	Start() error
}

type Config struct {
	Logger    *zap.Logger
	Functions      map[string]function.Function
	Constants      map[string]cty.Value
	evalCtx        *hcl.EvalContext
	BaseDir        string
	WriteDir string

	SigActions     *SignalActionHandler
	Startables     []Startable
	BusCapsuleType cty.Type
	CtyBusMap      map[string]cty.Value
	Buses          map[string]bus.EventBus
	CtyClientMap   map[string]cty.Value
	Clients        map[string]map[string]Client
	CtyServerMap   map[string]cty.Value
	Servers        map[string]map[string]Listener
	CtyVarMap      map[string]cty.Value

	MetricsServers map[string]*MetricsServer

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

func (c *ConfigBuilder) WithWriteDir(writeDir string) *ConfigBuilder {
	c.writeDir = writeDir
	return c
}

func (cb *ConfigBuilder) Build() (*Config, hcl.Diagnostics) {
	config := &Config{
		Logger:         cb.logger,
		BaseDir:   cb.baseDir,
		WriteDir:  cb.writeDir,
		Constants:      make(map[string]cty.Value),
		SigActions:   NewSignalActionHandler(cb.logger),
		Buses:        make(map[string]bus.EventBus),
		CtyBusMap:    make(map[string]cty.Value),
		Clients:      make(map[string]map[string]Client),
		CtyClientMap: make(map[string]cty.Value),
		Servers:      make(map[string]map[string]Listener),
		CtyServerMap: make(map[string]cty.Value),
		CtyVarMap:      make(map[string]cty.Value),
		MetricsServers: make(map[string]*MetricsServer),
		Crons:          make(map[string]*cron.Cron),
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

	// Add environment variables to the evaluation context
	config.Constants["env"] = GetEnvObject()
	config.Constants["httpstatus"] = getStatusCodeObject()
	config.Constants["sys"] = GetSysObject(config.BaseDir, config.WriteDir)

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

	for name, function := range functions.GetMcpFunctions() {
		funcs[name] = function
	}

	for name, fn := range randcty.GetRandomFunctions() {
		funcs[name] = fn
	}

	funcs["call"] = CallFunction(c)
	funcs["llm_wrap"] = functions.LLMWrapFunc
	funcs["diff"] = functions.DiffFunc
	funcs["error"] = functions.ErrorFunc
	funcs["patch"] = functions.PatchFunc
	funcs["send"] = SendFunction(c)
	funcs["sendjson"] = SendJSONFunction(c)
	funcs["sendgo"] = SendGoFunction(c)
	funcs["typeof"] = functions.TypeOfFunc

	for name, fn := range GetVariableFunctions() {
		funcs[name] = fn
	}

	for name, fn := range timecty.GetTimeFunctions() {
		funcs[name] = fn
	}

	if c.BaseDir != "" {
		funcs["abspath"] = filesystem.AbsPathFunc
		funcs["basename"] = filesystem.BasenameFunc
		funcs["dirname"] = filesystem.DirnameFunc
		funcs["file"] = filesystem.MakeFileFunc(c.BaseDir, false)
		funcs["fileexists"] = filesystem.MakeFileExistsFunc(c.BaseDir)
		funcs["fileset"] = filesystem.MakeFileSetFunc(c.BaseDir)
		funcs["filebase64"] = filesystem.MakeFileFunc(c.BaseDir, true)
		funcs["pathexpand"] = filesystem.PathExpandFunc
	}

	if c.WriteDir != "" {
		funcs["filewrite"] = functions.MakeFileWriteFunc(c.WriteDir)
		funcs["fileappend"] = functions.MakeFileAppendFunc(c.WriteDir)
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

	// templatefile is registered last so its funcsGetter closure sees the fully
	// populated funcs map (including user-defined functions) at evaluation time.
	if c.BaseDir != "" {
		funcsGetter := func() map[string]function.Function {
			result := make(map[string]function.Function, len(funcs))
			for k, v := range funcs {
				if k != "templatefile" {
					result[k] = v
				}
			}
			return result
		}
		funcs["templatefile"] = functions.MakeTemplateFileFunc(c.BaseDir, c.Constants, funcsGetter)
		funcs["gotemplatefile"] = functions.MakeGoTemplateFileFunc(c.BaseDir, c.Constants)
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
