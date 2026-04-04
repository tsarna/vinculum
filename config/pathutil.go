package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeResolvePath resolves path relative to baseDir and verifies the result is
// contained within baseDir, rejecting directory traversal and home directory expansion.
func SafeResolvePath(baseDir, path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("home directory expansion (~) is not permitted in file write functions")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)

	cleanBase := filepath.Clean(baseDir)
	rel, err := filepath.Rel(cleanBase, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the permitted base directory %q", path, baseDir)
	}

	return path, nil
}
