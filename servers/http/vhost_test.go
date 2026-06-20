package httpserver_test

import (
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

//go:embed testdata/vhost_handle.vcl
var vhostHandleVCL []byte

//go:embed testdata/vhost_files_method.vcl
var vhostFilesMethodVCL []byte

//go:embed testdata/vhost_files_nopath.vcl
var vhostFilesNoPathVCL []byte

//go:embed testdata/vhost_dup_pattern.vcl
var vhostDupPatternVCL []byte

// requestHost issues a GET to path with the given Host header and returns the
// response body.
func requestHost(t *testing.T, srv *httpserver.HttpServer, host, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)
	body, err := io.ReadAll(w.Result().Body)
	require.NoError(t, err)
	return string(body)
}

// --- Host-scoped handle routing ---

func TestVHostHandleRouting(t *testing.T) {
	srv := buildHTTPServer(t, vhostHandleVCL)

	// Each host reaches its own handler.
	assert.Equal(t, "api", requestHost(t, srv, "api.example.com", "/where"))
	assert.Equal(t, "cdn", requestHost(t, srv, "cdn.example.com", "/where"))

	// An unmatched host falls through to the host-less catch-all.
	assert.Equal(t, "default", requestHost(t, srv, "other.example.com", "/where"))
}

// --- Host-scoped files: same urlpath, different hosts, different on-disk dirs ---

func TestVHostFilesScoping(t *testing.T) {
	base := t.TempDir()

	apiDir := filepath.Join(base, "api")
	cdnDir := filepath.Join(base, "cdn")
	require.NoError(t, os.MkdirAll(apiDir, 0o755))
	require.NoError(t, os.MkdirAll(cdnDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(apiDir, "file.txt"), []byte("api-content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cdnDir, "file.txt"), []byte("cdn-content"), 0o644))

	source := fmt.Sprintf(`server "http" "main" {
  listen = "127.0.0.1:0"

  files "api.example.com/static" {
    directory = %q
  }

  files "cdn.example.com/static" {
    directory = %q
  }
}
`, apiDir, cdnDir)

	c, diags := cfg.NewConfig().
		WithSources([]byte(source)).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", base).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())
	srv := c.Servers["http"]["main"].(*httpserver.HttpServer)

	// Each host serves its own directory; verifies path-only StripPrefix
	// (the request URL "/static/file.txt" carries no host).
	assert.Equal(t, "api-content", requestHost(t, srv, "api.example.com", "/static/file.txt"))
	assert.Equal(t, "cdn-content", requestHost(t, srv, "cdn.example.com", "/static/file.txt"))
}

// --- Error cases: must produce diagnostics, never panic ---

func TestVHostFilesMethodRejected(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources(vhostFilesMethodVCL).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", "/tmp").
		Build()
	require.True(t, diags.HasErrors(), "method on files block should fail")
	assert.Contains(t, diags.Error(), "method")
}

func TestVHostFilesNoPathRejected(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources(vhostFilesNoPathVCL).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", "/tmp").
		Build()
	require.True(t, diags.HasErrors(), "files urlpath without a path should fail")
	assert.Contains(t, diags.Error(), "path")
}

func TestVHostDuplicatePatternRejected(t *testing.T) {
	// A conflicting registration would panic in ServeMux.Handle; safeMuxHandle
	// must convert it to a diagnostic instead of crashing.
	_, diags := cfg.NewConfig().
		WithSources(vhostDupPatternVCL).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "duplicate pattern should fail")
	assert.Contains(t, diags.Error(), "conflicting")
}
