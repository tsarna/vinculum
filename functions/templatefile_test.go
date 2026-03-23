package functions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func writeTemplate(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return name
}

func makeTestTemplateFileFunc(t *testing.T, baseDir string, constants map[string]cty.Value) function.Function {
	t.Helper()
	if constants == nil {
		constants = map[string]cty.Value{}
	}
	// funcsGetter returns empty map for tests (no stdlib needed for basic template tests)
	return MakeTemplateFileFunc(baseDir, constants, func() map[string]function.Function {
		return map[string]function.Function{}
	})
}

func TestTemplateFileStringInterpolation(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "hello.tmpl", "Hello, ${name}!")

	fn := makeTestTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("hello.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("world")}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("Hello, world!"), result)
}

func TestTemplateFileNumberVar(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "num.tmpl", "${count + 1}")

	fn := makeTestTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("num.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"count": cty.NumberIntVal(41)}),
	})
	require.NoError(t, err)
	// Single interpolation of a number returns number
	bf := result.AsBigFloat()
	n, _ := bf.Int64()
	assert.Equal(t, int64(42), n)
}

func TestTemplateFileConditional(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "cond.tmpl", "%{ if flag }yes%{ else }no%{ endif }")

	fn := makeTestTemplateFileFunc(t, base, nil)

	result, err := fn.Call([]cty.Value{
		cty.StringVal("cond.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"flag": cty.True}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("yes"), result)

	result, err = fn.Call([]cty.Value{
		cty.StringVal("cond.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"flag": cty.False}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("no"), result)
}

func TestTemplateFileConstants(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "const.tmpl", "os=${platform}")

	constants := map[string]cty.Value{
		"platform": cty.StringVal("linux"),
	}
	fn := makeTestTemplateFileFunc(t, base, constants)

	result, err := fn.Call([]cty.Value{
		cty.StringVal("const.tmpl"),
		cty.ObjectVal(map[string]cty.Value{}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("os=linux"), result)
}

func TestTemplateFileVarsShadowConstants(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "shadow.tmpl", "${platform}")

	constants := map[string]cty.Value{"platform": cty.StringVal("linux")}
	fn := makeTestTemplateFileFunc(t, base, constants)

	// vars.platform should shadow constants.platform
	result, err := fn.Call([]cty.Value{
		cty.StringVal("shadow.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"platform": cty.StringVal("darwin")}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("darwin"), result)
}

func TestTemplateFileEmptyVars(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "static.tmpl", "no vars here")

	fn := makeTestTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("static.tmpl"),
		cty.EmptyObjectVal,
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("no vars here"), result)
}

func TestTemplateFileMissingFile(t *testing.T) {
	base := t.TempDir()
	fn := makeTestTemplateFileFunc(t, base, nil)

	_, err := fn.Call([]cty.Value{
		cty.StringVal("nonexistent.tmpl"),
		cty.EmptyObjectVal,
	})
	assert.Error(t, err)
}

func TestTemplateFileInvalidSyntax(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "bad.tmpl", "${unclosed")

	fn := makeTestTemplateFileFunc(t, base, nil)
	_, err := fn.Call([]cty.Value{
		cty.StringVal("bad.tmpl"),
		cty.EmptyObjectVal,
	})
	assert.Error(t, err)
}

func TestTemplateFileInvalidVarName(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "x.tmpl", "x")

	fn := makeTestTemplateFileFunc(t, base, nil)
	_, err := fn.Call([]cty.Value{
		cty.StringVal("x.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"123invalid": cty.StringVal("x")}),
	})
	assert.Error(t, err)
}

func TestTemplateFileNonObjectVars(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "x.tmpl", "x")

	fn := makeTestTemplateFileFunc(t, base, nil)
	_, err := fn.Call([]cty.Value{
		cty.StringVal("x.tmpl"),
		cty.StringVal("not an object"),
	})
	assert.Error(t, err)
}

// --- gotemplatefile tests ---

func makeTestGoTemplateFileFunc(t *testing.T, baseDir string, constants map[string]cty.Value) function.Function {
	t.Helper()
	if constants == nil {
		constants = map[string]cty.Value{}
	}
	return MakeGoTemplateFileFunc(baseDir, constants)
}

func TestGoTemplateFileStringInterpolation(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "hello.tmpl", "Hello, {{.name}}!")

	fn := makeTestGoTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("hello.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("world")}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("Hello, world!"), result)
}

func TestGoTemplateFileConditional(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "cond.tmpl", "{{if .flag}}yes{{else}}no{{end}}")

	fn := makeTestGoTemplateFileFunc(t, base, nil)

	result, err := fn.Call([]cty.Value{
		cty.StringVal("cond.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"flag": cty.True}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("yes"), result)

	result, err = fn.Call([]cty.Value{
		cty.StringVal("cond.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"flag": cty.False}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("no"), result)
}

func TestGoTemplateFileRange(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "range.tmpl", "{{range .items}}{{.}} {{end}}")

	fn := makeTestGoTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("range.tmpl"),
		cty.ObjectVal(map[string]cty.Value{
			"items": cty.ListVal([]cty.Value{
				cty.StringVal("a"),
				cty.StringVal("b"),
				cty.StringVal("c"),
			}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("a b c "), result)
}

func TestGoTemplateFileConstants(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "const.tmpl", "os={{.platform}}")

	constants := map[string]cty.Value{
		"platform": cty.StringVal("linux"),
	}
	fn := makeTestGoTemplateFileFunc(t, base, constants)

	result, err := fn.Call([]cty.Value{
		cty.StringVal("const.tmpl"),
		cty.EmptyObjectVal,
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("os=linux"), result)
}

func TestGoTemplateFileVarsShadowConstants(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "shadow.tmpl", "{{.platform}}")

	constants := map[string]cty.Value{"platform": cty.StringVal("linux")}
	fn := makeTestGoTemplateFileFunc(t, base, constants)

	result, err := fn.Call([]cty.Value{
		cty.StringVal("shadow.tmpl"),
		cty.ObjectVal(map[string]cty.Value{"platform": cty.StringVal("darwin")}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("darwin"), result)
}

func TestGoTemplateFileEmptyVars(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "static.tmpl", "no vars here")

	fn := makeTestGoTemplateFileFunc(t, base, nil)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("static.tmpl"),
		cty.EmptyObjectVal,
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("no vars here"), result)
}

func TestGoTemplateFileMissingFile(t *testing.T) {
	base := t.TempDir()
	fn := makeTestGoTemplateFileFunc(t, base, nil)

	_, err := fn.Call([]cty.Value{
		cty.StringVal("nonexistent.tmpl"),
		cty.EmptyObjectVal,
	})
	assert.Error(t, err)
}

func TestGoTemplateFileInvalidSyntax(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "bad.tmpl", "{{unclosed")

	fn := makeTestGoTemplateFileFunc(t, base, nil)
	_, err := fn.Call([]cty.Value{
		cty.StringVal("bad.tmpl"),
		cty.EmptyObjectVal,
	})
	assert.Error(t, err)
}

func TestGoTemplateFileNonObjectVars(t *testing.T) {
	base := t.TempDir()
	writeTemplate(t, base, "x.tmpl", "x")

	fn := makeTestGoTemplateFileFunc(t, base, nil)
	_, err := fn.Call([]cty.Value{
		cty.StringVal("x.tmpl"),
		cty.StringVal("not an object"),
	})
	assert.Error(t, err)
}
