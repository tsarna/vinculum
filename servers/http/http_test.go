package httpserver_test

import (
	_ "embed"
	"strings"
	"testing"

	cfg "github.com/tsarna/vinculum/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

//go:embed testdata/http_files_abs.vcl
var httpFilesAbs []byte

//go:embed testdata/http_files_rel_no_basedir.vcl
var httpFilesRelNoBasedir []byte

//go:embed testdata/http_files_rel_with_basedir.vcl
var httpFilesRelWithBasedir []byte

func TestHttpFilesAbsoluteDirectoryNoFilePath(t *testing.T) {
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().WithSources(httpFilesAbs).WithLogger(logger).Build()
	require.True(t, diags.HasErrors(), "files block without --file-path should fail")
	assert.True(t, strings.Contains(diags.Error(), "--file-path"), "error should mention --file-path")
}

func TestHttpFilesAbsoluteDirectoryWithFilePath(t *testing.T) {
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().
		WithSources(httpFilesAbs).
		WithLogger(logger).
		WithFeature("readfiles", "/tmp").
		Build()
	require.False(t, diags.HasErrors(), "absolute directory with --file-path should succeed: %v", diags)
}

func TestHttpFilesRelativeDirectoryNoFilePath(t *testing.T) {
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().WithSources(httpFilesRelNoBasedir).WithLogger(logger).Build()
	require.True(t, diags.HasErrors(), "relative directory without --file-path should fail")
	assert.True(t, strings.Contains(diags.Error(), "--file-path"), "error should mention --file-path")
}

func TestHttpFilesRelativeDirectoryWithFilePath(t *testing.T) {
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().
		WithSources(httpFilesRelWithBasedir).
		WithLogger(logger).
		WithFeature("readfiles", "/tmp").
		Build()
	require.False(t, diags.HasErrors(), "relative directory with --file-path should succeed: %v", diags)
}
