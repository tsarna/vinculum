package functions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// safeResolvePath resolves path relative to baseDir and verifies the result is
// contained within baseDir, rejecting directory traversal attempts.
func safeResolvePath(baseDir, path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("home directory expansion (~) is not permitted in file write functions")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)

	cleanBase := filepath.Clean(baseDir)
	rel, err := filepath.Rel(cleanBase, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is outside the permitted base directory %q", path, baseDir)
	}

	return path, nil
}

// MakeFileWriteFunc constructs a function that writes a string to a file,
// creating or overwriting it. The path must be within baseDir.
func MakeFileWriteFunc(baseDir string) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "path", Type: cty.String},
			{Name: "content", Type: cty.String},
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			path, err := safeResolvePath(baseDir, args[0].AsString())
			if err != nil {
				return cty.False, err
			}
			content := args[1].AsString()
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return cty.False, fmt.Errorf("failed to write %s: %w", path, err)
			}
			return cty.True, nil
		},
	})
}

// MakeFileAppendFunc constructs a function that appends a string to a file,
// creating the file if it does not exist. The path must be within baseDir.
func MakeFileAppendFunc(baseDir string) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "path", Type: cty.String},
			{Name: "content", Type: cty.String},
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			path, err := safeResolvePath(baseDir, args[0].AsString())
			if err != nil {
				return cty.False, err
			}
			content := args[1].AsString()
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return cty.False, fmt.Errorf("failed to open %s for append: %w", path, err)
			}
			defer f.Close()
			if _, err := f.WriteString(content); err != nil {
				return cty.False, fmt.Errorf("failed to append to %s: %w", path, err)
			}
			return cty.True, nil
		},
	})
}
