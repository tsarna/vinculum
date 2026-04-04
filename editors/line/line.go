package line

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterEditorType("line", processLineEditor)
}

// --- Config-time structs ---

type lineEditorBody struct {
	Mode    string        `hcl:"mode,optional"`
	Before  *contentBlock `hcl:"before,block"`
	After   *contentBlock `hcl:"after,block"`
	Matches []matchBlock  `hcl:"match,block"`
}

type contentBlock struct {
	Content hcl.Expression `hcl:"content"`
}

type matchBlock struct {
	Pattern  string         `hcl:",label"`
	Required hcl.Expression `hcl:"required,optional"`
	Max      hcl.Expression `hcl:"max,optional"`
	When     hcl.Expression `hcl:"when,optional"`
	Replace  hcl.Expression `hcl:"replace,optional"`
	Abort    hcl.Expression `hcl:"abort,optional"`
}

// compiledRule is a match rule with the regex pre-compiled and required/max resolved.
type compiledRule struct {
	re       *regexp.Regexp
	required int            // minimum match count required
	max      int            // 0 = unlimited
	when     hcl.Expression // nil if absent
	replace  hcl.Expression // nil if absent
	abort    hcl.Expression // nil if absent
}

// lineEditor holds everything needed at runtime for one editor "line" block.
type lineEditor struct {
	config         *cfg.Config
	evalCtxFn      func() *hcl.EvalContext
	name           string
	mode           string // "file" or "string"
	params         []string
	variadicParam  string
	backup         string
	createIfAbsent bool
	before         hcl.Expression // nil if absent
	after          hcl.Expression // nil if absent
	rules          []compiledRule
}

// processLineEditor is called at config time to compile an editor "line" block.
func processLineEditor(config *cfg.Config, evalCtxFn func() *hcl.EvalContext, def *cfg.EditorDefinition) (function.Function, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	body := &lineEditorBody{}
	decodeDiags := gohcl.DecodeBody(def.Body, evalCtxFn(), body)
	diags = diags.Extend(decodeDiags)
	if diags.HasErrors() {
		return function.Function{}, diags
	}

	mode := body.Mode
	if mode == "" {
		mode = "file"
	}

	switch mode {
	case "file", "string":
		// valid
	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid editor mode",
			Detail:   fmt.Sprintf("editor mode must be \"file\" or \"string\", got %q", mode),
			Subject:  def.DefRange.Ptr(),
		})
		return function.Function{}, diags
	}

	if mode == "file" && config.WriteDir == "" {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "editor block requires writefiles",
			Detail:   "editor blocks with mode = \"file\" require the --write-path flag to be set",
			Subject:  def.DefRange.Ptr(),
		})
		return function.Function{}, diags
	}

	ed := &lineEditor{
		config:        config,
		evalCtxFn:     evalCtxFn,
		name:          def.Name,
		mode:          mode,
		params:        def.Params,
		variadicParam: def.VariadicParam,
	}

	if body.Before != nil {
		ed.before = body.Before.Content
	}
	if body.After != nil {
		ed.after = body.After.Content
	}

	for i, m := range body.Matches {
		re, err := regexp.Compile(m.Pattern)
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid regex in match block",
				Detail:   fmt.Sprintf("match[%d] regex %q: %s", i, m.Pattern, err),
				Subject:  def.DefRange.Ptr(),
			})
			continue
		}

		required := 0
		if m.Required != nil {
			val, valDiags := m.Required.Value(evalCtxFn())
			diags = diags.Extend(valDiags)
			if !valDiags.HasErrors() {
				switch val.Type() {
				case cty.Bool:
					if val.True() {
						required = 1
					}
				case cty.Number:
					bf := val.AsBigFloat()
					n, _ := bf.Int64()
					required = int(n)
				}
			}
		}

		max := 0
		if m.Max != nil {
			val, valDiags := m.Max.Value(evalCtxFn())
			diags = diags.Extend(valDiags)
			if !valDiags.HasErrors() && val.Type() == cty.Number {
				bf := val.AsBigFloat()
				n, _ := bf.Int64()
				max = int(n)
			}
		}

		ed.rules = append(ed.rules, compiledRule{
			re:       re,
			required: required,
			max:      max,
			when:     m.When,
			replace:  m.Replace,
			abort:    m.Abort,
		})
	}

	if diags.HasErrors() {
		return function.Function{}, diags
	}

	return ed.makeFunc(), diags
}

// makeFunc builds the cty function for this line editor.
func (ed *lineEditor) makeFunc() function.Function {
	params := []function.Parameter{
		{Name: "ctx", Type: cty.DynamicPseudoType},
	}

	var retType cty.Type
	if ed.mode == "string" {
		params = append(params, function.Parameter{Name: "input", Type: cty.String})
		retType = cty.String
	} else {
		params = append(params, function.Parameter{Name: "filename", Type: cty.String})
		retType = cty.Bool
	}

	for _, p := range ed.params {
		params = append(params, function.Parameter{Name: p, Type: cty.DynamicPseudoType})
	}

	implFn := ed.impl
	if ed.mode == "string" {
		implFn = ed.implString
	}

	spec := &function.Spec{
		Params: params,
		Type:   function.StaticReturnType(retType),
		Impl:   implFn,
	}

	if ed.variadicParam != "" {
		spec.VarParam = &function.Parameter{Name: ed.variadicParam, Type: cty.DynamicPseudoType}
	}

	return function.New(spec)
}

// userParamsFromArgs extracts the user-declared parameter values from the args slice.
// args[0] = ctx, args[1] = filename or input, args[2+] = user params.
func (ed *lineEditor) userParamsFromArgs(args []cty.Value) map[string]cty.Value {
	userParams := make(map[string]cty.Value, len(ed.params))
	for i, p := range ed.params {
		userParams[p] = args[2+i]
	}
	if ed.variadicParam != "" {
		varArgs := args[2+len(ed.params):]
		if len(varArgs) > 0 {
			varVals := make([]cty.Value, len(varArgs))
			copy(varVals, varArgs)
			userParams[ed.variadicParam] = cty.TupleVal(varVals)
		} else {
			userParams[ed.variadicParam] = cty.EmptyObjectVal
		}
	}
	return userParams
}

// runRules processes lines from scanner (nil = no lines) through the configured rules,
// writing output to w. Returns whether any content changed relative to the input,
// and whether a soft-abort occurred (abort expr fired or required constraint not met).
func (ed *lineEditor) runRules(
	goCtx context.Context,
	w io.Writer,
	scanner *bufio.Scanner,
	filename string,
	userParams map[string]cty.Value,
) (changed bool, softAbort bool, err error) {
	// Write before block
	if ed.before != nil {
		evalCtx := ed.buildBeforeAfterCtx(goCtx, filename, userParams)
		val, evalErr := ed.evalStringExpr(ed.before, evalCtx)
		if evalErr != nil {
			return false, false, fmt.Errorf("before block: %w", evalErr)
		}
		if val != "" {
			changed = true
			if _, writeErr := io.WriteString(w, val); writeErr != nil {
				return false, false, fmt.Errorf("writing before block: %w", writeErr)
			}
		}
	}

	// Per-rule match counters
	matchCounts := make([]int, len(ed.rules))

	// Process lines
	if scanner != nil {
		lineno := 0
		for scanner.Scan() {
			lineno++
			line := scanner.Text() + "\n"

			matched := false
			for ri, rule := range ed.rules {
				if rule.max > 0 && matchCounts[ri] >= rule.max {
					continue
				}

				if rule.when != nil {
					whenCtx := ed.buildPreMatchCtx(goCtx, filename, lineno, userParams)
					whenVal, whenErr := rule.when.Value(whenCtx)
					if whenErr != nil {
						return false, false, fmt.Errorf("line %d when expression: %w", lineno, whenErr)
					}
					if whenVal.IsNull() || !whenVal.IsKnown() || (whenVal.Type() == cty.Bool && whenVal.False()) {
						continue
					}
				}

				groups := rule.re.FindStringSubmatch(line)
				if groups == nil {
					continue
				}

				matched = true
				matchCounts[ri]++

				matchCtx := ed.buildMatchCtx(goCtx, filename, lineno, line, groups, rule.re, matchCounts[ri], userParams)

				if rule.abort != nil {
					abortVal, abortErr := rule.abort.Value(matchCtx)
					if abortErr != nil {
						return false, false, fmt.Errorf("line %d abort expression: %w", lineno, abortErr)
					}
					if abortVal.IsKnown() && !abortVal.IsNull() && abortVal.Type() == cty.Bool && abortVal.True() {
						return false, true, nil
					}
				}

				var output string
				if rule.replace != nil {
					output, err = ed.evalStringExpr(rule.replace, matchCtx)
					if err != nil {
						return false, false, fmt.Errorf("line %d replace expression: %w", lineno, err)
					}
					if output != line {
						changed = true
					}
				} else {
					output = line
				}

				if _, writeErr := io.WriteString(w, output); writeErr != nil {
					return false, false, fmt.Errorf("writing line %d: %w", lineno, writeErr)
				}
				break
			}

			if !matched {
				if _, writeErr := io.WriteString(w, line); writeErr != nil {
					return false, false, fmt.Errorf("writing line %d: %w", lineno, writeErr)
				}
			}
		}

		if scanErr := scanner.Err(); scanErr != nil {
			return false, false, fmt.Errorf("reading input: %w", scanErr)
		}
	}

	// Check required constraints
	for ri, rule := range ed.rules {
		if rule.required > 0 && matchCounts[ri] < rule.required {
			return false, true, nil
		}
	}

	// Write after block
	if ed.after != nil {
		evalCtx := ed.buildBeforeAfterCtx(goCtx, filename, userParams)
		val, evalErr := ed.evalStringExpr(ed.after, evalCtx)
		if evalErr != nil {
			return false, false, fmt.Errorf("after block: %w", evalErr)
		}
		if val != "" {
			changed = true
			if _, writeErr := io.WriteString(w, val); writeErr != nil {
				return false, false, fmt.Errorf("writing after block: %w", writeErr)
			}
		}
	}

	return changed, false, nil
}

// impl is the runtime implementation for mode = "file".
func (ed *lineEditor) impl(args []cty.Value, _ cty.Type) (cty.Value, error) {
	goCtx, err := ctyutil.GetContextFromValue(args[0])
	if err != nil {
		return cty.False, fmt.Errorf("editor %s: %w", ed.name, err)
	}

	filePath, err := cfg.SafeResolvePath(ed.config.WriteDir, args[1].AsString())
	if err != nil {
		return cty.False, fmt.Errorf("editor %s: %w", ed.name, err)
	}

	userParams := ed.userParamsFromArgs(args)

	// Open original file (or handle create_if_absent)
	origFile, err := os.Open(filePath)
	fileExists := true
	if err != nil {
		if os.IsNotExist(err) && ed.createIfAbsent {
			fileExists = false
		} else {
			return cty.False, fmt.Errorf("editor %s: opening %s: %w", ed.name, filePath, err)
		}
	}

	// Create temp file in same directory
	dir := filepath.Dir(filePath)
	tmpFile, err := os.CreateTemp(dir, ".tmp*")
	if err != nil {
		if origFile != nil {
			origFile.Close()
		}
		return cty.False, fmt.Errorf("editor %s: creating temp file: %w", ed.name, err)
	}
	tmpPath := tmpFile.Name()

	cleanup := func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}

	// Copy permissions from original file
	if fileExists {
		if fi, statErr := os.Stat(filePath); statErr == nil {
			os.Chmod(tmpPath, fi.Mode()) //nolint:errcheck
			if uid, gid, ok := fileOwnership(fi); ok {
				os.Lchown(tmpPath, uid, gid) //nolint:errcheck // best-effort; requires root
			}
		}
	}

	// Build scanner over original file (nil if file doesn't exist)
	var scanner *bufio.Scanner
	if fileExists {
		scanner = bufio.NewScanner(origFile)
	}

	changed, softAbort, runErr := ed.runRules(goCtx, tmpFile, scanner, filePath, userParams)

	if origFile != nil {
		origFile.Close()
	}

	if runErr != nil {
		cleanup()
		return cty.False, fmt.Errorf("editor %s: %w", ed.name, runErr)
	}
	if softAbort {
		cleanup()
		return cty.False, nil
	}

	// If nothing changed (and not a fresh creation), discard and return false
	if !changed && fileExists {
		cleanup()
		return cty.False, nil
	}

	if closeErr := tmpFile.Close(); closeErr != nil {
		os.Remove(tmpPath)
		return cty.False, fmt.Errorf("editor %s: closing temp file: %w", ed.name, closeErr)
	}

	// Create backup via hard link before rename
	if ed.backup != "" && fileExists {
		backupPath := filePath + ed.backup
		os.Remove(backupPath) //nolint:errcheck // remove stale backup
		if linkErr := os.Link(filePath, backupPath); linkErr != nil {
			os.Remove(tmpPath)
			return cty.False, fmt.Errorf("editor %s: creating backup %s: %w", ed.name, backupPath, linkErr)
		}
	}

	// Atomically rename temp file over original
	if renameErr := os.Rename(tmpPath, filePath); renameErr != nil {
		os.Remove(tmpPath)
		return cty.False, fmt.Errorf("editor %s: renaming temp file to %s: %w", ed.name, filePath, renameErr)
	}

	return cty.True, nil
}

// implString is the runtime implementation for mode = "string".
func (ed *lineEditor) implString(args []cty.Value, _ cty.Type) (cty.Value, error) {
	goCtx, err := ctyutil.GetContextFromValue(args[0])
	if err != nil {
		return cty.NullVal(cty.String), fmt.Errorf("editor %s: %w", ed.name, err)
	}

	input := args[1].AsString()
	userParams := ed.userParamsFromArgs(args)

	var buf strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(input))

	_, softAbort, runErr := ed.runRules(goCtx, &buf, scanner, "", userParams)
	if runErr != nil {
		return cty.NullVal(cty.String), fmt.Errorf("editor %s: %w", ed.name, runErr)
	}
	if softAbort {
		return cty.NullVal(cty.String), fmt.Errorf("editor %s: aborted", ed.name)
	}

	return cty.StringVal(buf.String()), nil
}

// buildBeforeAfterCtx builds an eval context for before/after blocks (no line context).
func (ed *lineEditor) buildBeforeAfterCtx(goCtx context.Context, filename string, userParams map[string]cty.Value) *hcl.EvalContext {
	ctxObj := ctyutil.NewContextObject(goCtx)
	ctxObj.WithStringAttribute("filename", filename)
	ctxObjVal, _ := ctxObj.Build()

	evalCtx := ed.evalCtxFn().NewChild()
	evalCtx.Variables = make(map[string]cty.Value, len(userParams)+1)
	evalCtx.Variables["ctx"] = ctxObjVal
	for k, v := range userParams {
		evalCtx.Variables[k] = v
	}
	return evalCtx
}

// buildPreMatchCtx builds an eval context for when expressions (pre-regex, no match info).
func (ed *lineEditor) buildPreMatchCtx(goCtx context.Context, filename string, lineno int, userParams map[string]cty.Value) *hcl.EvalContext {
	ctxObj := ctyutil.NewContextObject(goCtx)
	ctxObj.WithStringAttribute("filename", filename)
	ctxObj.WithInt64Attribute("lineno", int64(lineno))
	ctxObjVal, _ := ctxObj.Build()

	evalCtx := ed.evalCtxFn().NewChild()
	evalCtx.Variables = make(map[string]cty.Value, len(userParams)+1)
	evalCtx.Variables["ctx"] = ctxObjVal
	for k, v := range userParams {
		evalCtx.Variables[k] = v
	}
	return evalCtx
}

// buildMatchCtx builds an eval context for replace/abort expressions (post-regex match).
func (ed *lineEditor) buildMatchCtx(goCtx context.Context, filename string, lineno int, line string, groups []string, re *regexp.Regexp, count int, userParams map[string]cty.Value) *hcl.EvalContext {
	ctxObj := ctyutil.NewContextObject(goCtx)
	ctxObj.WithStringAttribute("filename", filename)
	ctxObj.WithInt64Attribute("lineno", int64(lineno))
	ctxObj.WithStringAttribute("line", line)
	ctxObj.WithInt64Attribute("count", int64(count))

	groupVals := make([]cty.Value, len(groups))
	for i, g := range groups {
		groupVals[i] = cty.StringVal(g)
	}
	if len(groupVals) > 0 {
		ctxObj.WithAttribute("groups", cty.ListVal(groupVals))
	} else {
		ctxObj.WithAttribute("groups", cty.ListValEmpty(cty.String))
	}

	namedMap := make(map[string]cty.Value)
	for i, name := range re.SubexpNames() {
		if name != "" && i < len(groups) {
			namedMap[name] = cty.StringVal(groups[i])
		}
	}
	if len(namedMap) > 0 {
		ctxObj.WithAttribute("named", cty.MapVal(namedMap))
	} else {
		ctxObj.WithAttribute("named", cty.MapValEmpty(cty.String))
	}

	ctxObjVal, _ := ctxObj.Build()

	evalCtx := ed.evalCtxFn().NewChild()
	evalCtx.Variables = make(map[string]cty.Value, len(userParams)+1)
	evalCtx.Variables["ctx"] = ctxObjVal
	for k, v := range userParams {
		evalCtx.Variables[k] = v
	}
	return evalCtx
}

// evalStringExpr evaluates an expression and returns its string value.
func (ed *lineEditor) evalStringExpr(expr hcl.Expression, evalCtx *hcl.EvalContext) (string, error) {
	val, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		return "", diags
	}
	if val.IsNull() || !val.IsKnown() {
		return "", nil
	}
	if val.Type() != cty.String {
		return "", fmt.Errorf("expression must return a string, got %s", val.Type().FriendlyName())
	}
	return val.AsString(), nil
}
