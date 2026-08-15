package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// disabledSubBlocksSource exercises the env-toggle pattern on both of the
// server's repeated sub-blocks. The disabled files block uses a relative
// directory with no --file-path configured: like real_ip's trusted_proxies
// check, that requirement must not fire for an inert block.
const disabledSubBlocksSource = `
server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/on" {
    action = "on"
  }

  handle "/off" {
    disabled = true
    action   = "off"
  }

  files "/static/" {
    disabled  = true
    directory = "www"
  }
}
`

// TestDisabledHandleAndFilesAreNotRouted confirms a disabled handle or files
// block is parsed but never registered on the mux, and that neither its own
// required fields nor the server-wide --file-path requirement are enforced.
func TestDisabledHandleAndFilesAreNotRouted(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources([]byte(disabledSubBlocksSource)).WithLogger(zap.NewNop()).Build()
	if diags.HasErrors() {
		t.Fatalf("build: %v", diags)
	}
	srv := c.Servers["http"]["main"].(*HttpServer)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/on", http.StatusOK},
		{"/off", http.StatusNotFound},
		{"/static/index.html", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("GET %s: status = %d, want %d", tc.path, w.Code, tc.want)
		}
	}
}
