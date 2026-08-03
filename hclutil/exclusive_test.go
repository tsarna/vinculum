package hclutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewEvalContextIsTheOnlyPathToACtx asserts that no production code builds
// a `ctx` object directly with richcty.NewContextObject.
//
// BuildEvalContext is what supplies ctx.auth, ctx.baggage, ctx.trace_id, and
// ctx.span_id. A site that assembles its own object gets none of them, silently
// — which is exactly what happened to the editor blocks: an editor called from
// an HTTP handler sat inside a live trace it had no way to read, and the schema
// grew a flag to describe the divergence rather than anyone noticing it was a
// bug.
//
// Tests are exempt: constructing a bare context value is how they make the
// argument an editor or bus function expects.
func TestNewEvalContextIsTheOnlyPathToACtx(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip this package (the sanctioned caller) and anything that is
			// not ours to police.
			switch d.Name() {
			case "hclutil", ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(src), "richcty.NewContextObject") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"build the ctx with hclutil.NewEvalContext(...).BuildEvalContext(...) instead; "+
			"a directly-assembled context object carries no auth, baggage, trace_id, or span_id")
}
