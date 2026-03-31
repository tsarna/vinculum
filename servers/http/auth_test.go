package httpserver_test

import (
	_ "embed"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

//go:embed testdata/auth_basic.vcl
var authBasicVCL []byte

//go:embed testdata/auth_none_override.vcl
var authNoneOverrideVCL []byte

//go:embed testdata/auth_custom.vcl
var authCustomVCL []byte

//go:embed testdata/auth_none_files.vcl
var authNoneFilesVCL []byte

func basicAuthHeader(username, password string) string {
	creds := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Basic " + creds
}

func buildHTTPServer(t *testing.T, source []byte) *httpserver.HttpServer {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources(source).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	srv, ok := c.Servers["http"]["main"].(*httpserver.HttpServer)
	require.True(t, ok, "expected *HttpServer in Servers[\"http\"][\"main\"]")
	return srv
}

// --- Basic auth ---

func TestBasicAuth_MissingCredentials(t *testing.T) {
	srv := buildHTTPServer(t, authBasicVCL)
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Basic realm="main"`, resp.Header.Get("WWW-Authenticate"))
}

func TestBasicAuth_WrongCredentials(t *testing.T) {
	srv := buildHTTPServer(t, authBasicVCL)
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", basicAuthHeader("alice", "wrong"))
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Basic realm="main"`, resp.Header.Get("WWW-Authenticate"))
}

func TestBasicAuth_CorrectCredentials(t *testing.T) {
	srv := buildHTTPServer(t, authBasicVCL)
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", basicAuthHeader("alice", "secret"))
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "alice", string(body))
}

// --- auth "none" overriding server-level auth ---

func TestAuthNoneOverride_PublicRouteNoCredentials(t *testing.T) {
	srv := buildHTTPServer(t, authNoneOverrideVCL)
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	// auth "none" on the handle disables server-level basic auth
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestAuthNoneOverride_PrivateRouteNoCredentials(t *testing.T) {
	srv := buildHTTPServer(t, authNoneOverrideVCL)
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	// No block-level auth → inherits server-level basic auth
	resp := w.Result()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Basic realm="main"`, resp.Header.Get("WWW-Authenticate"))
}

// --- auth "none" on files block ---

func TestAuthNoneFiles_NoCredentials(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(authNoneFilesVCL).WithLogger(zap.NewNop()).
		WithFeature("readfiles", "/tmp").Build()
	require.False(t, diags.HasErrors(), diags.Error())
	srv := c.Servers["http"]["main"].(*httpserver.HttpServer)
	req := httptest.NewRequest(http.MethodGet, "/public/", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	// auth "none" on the files block disables server-level basic auth
	// File server returns 200 or 301 (directory listing/redirect), not 401
	assert.NotEqual(t, http.StatusUnauthorized, w.Result().StatusCode)
}

// --- Custom auth ---

func TestCustomAuth_NullReturn(t *testing.T) {
	srv := buildHTTPServer(t, authCustomVCL)
	req := httptest.NewRequest(http.MethodGet, "/fail", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestCustomAuth_ObjectReturn(t *testing.T) {
	srv := buildHTTPServer(t, authCustomVCL)
	req := httptest.NewRequest(http.MethodGet, "/succeed", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "user", string(body))
}

func TestCustomAuth_HTTPRedirect(t *testing.T) {
	srv := buildHTTPServer(t, authCustomVCL)
	req := httptest.NewRequest(http.MethodGet, "/redirect", nil)
	w := httptest.NewRecorder()
	srv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://example.com/login", resp.Header.Get("Location"))
}
