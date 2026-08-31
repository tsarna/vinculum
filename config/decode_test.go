package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testWillDef struct {
	Topic    hcl.Expression `hcl:"topic"`
	Payload  hcl.Expression `hcl:"payload,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type testDecodeDef struct {
	Bus      hcl.Expression `hcl:"bus"`
	Optional hcl.Expression `hcl:"opt,optional"`
	Plain    string         `hcl:"plain,optional"`
	Will     *testWillDef   `hcl:"will,block"`
}

func decodeTestBody(t *testing.T, src string) (*testDecodeDef, hcl.Diagnostics) {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(src), "test.vcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), diags.Error())

	def := &testDecodeDef{}
	return def, DecodeBody(f.Body, nil, def)
}

// TestDecodeBodyEnforcesRequiredExpressions covers the whole point of the
// wrapper: gohcl's ImpliedBodySchema never marks an hcl.Expression attribute
// required, so without this every one of these cases decoded silently.
func TestDecodeBodyEnforcesRequiredExpressions(t *testing.T) {
	t.Run("omitted required expression is reported", func(t *testing.T) {
		_, diags := decodeTestBody(t, "opt = 1\n")

		require.True(t, diags.HasErrors())
		require.Len(t, diags, 1)
		assert.Equal(t, "Missing required argument", diags[0].Summary)
		assert.Equal(t, `The argument "bus" is required, but no definition was found.`, diags[0].Detail)
	})

	t.Run("provided required expression is accepted", func(t *testing.T) {
		def, diags := decodeTestBody(t, "bus = 1\n")

		assert.False(t, diags.HasErrors(), diags.Error())
		assert.True(t, IsExpressionProvided(def.Bus))
	})

	t.Run("omitted optional expression is not reported", func(t *testing.T) {
		def, diags := decodeTestBody(t, "bus = 1\n")

		assert.False(t, diags.HasErrors(), diags.Error())
		assert.False(t, IsExpressionProvided(def.Optional))
	})

	// A non-expression field keeps gohcl's own enforcement; the wrapper must
	// not double-report it.
	t.Run("non-expression required attributes are left to gohcl", func(t *testing.T) {
		_, diags := decodeTestBody(t, "plain = \"x\"\n")

		require.True(t, diags.HasErrors())
		require.Len(t, diags, 1)
		assert.Equal(t, `The argument "bus" is required, but no definition was found.`, diags[0].Detail)
	})
}

// TestDecodeBodyRecursesIntoBlocks is the half a top-level-only check would
// miss: an mqtt `will` with no `topic`, a redis `channel_subscription` with no
// `channel`.
func TestDecodeBodyRecursesIntoBlocks(t *testing.T) {
	t.Run("nested block reports against its own header", func(t *testing.T) {
		_, diags := decodeTestBody(t, "bus = 1\n\nwill {\n  payload = 2\n}\n")

		require.True(t, diags.HasErrors())
		require.Len(t, diags, 1)
		assert.Equal(t, `The argument "topic" is required, but no definition was found.`, diags[0].Detail)

		// The block's own def_range, not the enclosing body's closing brace —
		// which for a file-level body would have been the last line.
		require.NotNil(t, diags[0].Subject)
		assert.Equal(t, 3, diags[0].Subject.Start.Line)
	})

	t.Run("absent optional block is not walked", func(t *testing.T) {
		_, diags := decodeTestBody(t, "bus = 1\n")

		assert.False(t, diags.HasErrors(), diags.Error())
	})

	t.Run("complete nested block is accepted", func(t *testing.T) {
		_, diags := decodeTestBody(t, "bus = 1\n\nwill {\n  topic = 2\n}\n")

		assert.False(t, diags.HasErrors(), diags.Error())
	})
}

// TestDecodeBodyIsTheOnlyPathToGohcl asserts that no production code decodes a
// block body with gohcl.DecodeBody directly. Going around config.DecodeBody
// silently drops `required` on every hcl.Expression attribute in that block —
// see the doc comment there — and nothing else would report it.
//
// The match is on the call — "gohcl.DecodeBody(" — so the several comments that
// discuss gohcl.DecodeBody's behavior in prose are not offenders.
//
// Tests are exempt: a test decoding a body to check gohcl's own behavior is
// asking about gohcl, not about a block.
func TestDecodeBodyIsTheOnlyPathToGohcl(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	sanctioned, err := filepath.Abs("decode.go")
	require.NoError(t, err)

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == sanctioned {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(src), "gohcl.DecodeBody(") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"decode with config.DecodeBody instead; gohcl.DecodeBody silently ignores "+
			"`required` on every hcl.Expression attribute (gohcl/schema.go), so a "+
			"missing one reaches the handler as a synthetic null")
}
