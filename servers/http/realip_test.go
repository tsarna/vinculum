package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// resolveWith builds a resolver from the given config and runs resolve against
// a request with the given peer (RemoteAddr) and header value.
func resolveWith(t *testing.T, c *realIPConfig, peer, headerName, headerVal string) string {
	t.Helper()
	r, diags := compileRealIP(c)
	if diags.HasErrors() {
		t.Fatalf("compileRealIP: %v", diags)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	if headerVal != "" {
		req.Header.Set(headerName, headerVal)
	}
	return r.resolve(req)
}

func TestRealIPResolve(t *testing.T) {
	tests := []struct {
		name      string
		trusted   []string
		header    string
		recursive bool
		peer      string
		hdrName   string
		hdrVal    string
		want      string
	}{
		{
			name:      "trusted peer, recursive, single hop",
			trusted:   []string{"10.0.0.0/8"},
			recursive: true,
			peer:      "10.0.0.7:5000",
			hdrName:   "X-Forwarded-For",
			hdrVal:    "203.0.113.5",
			want:      "203.0.113.5",
		},
		{
			name:      "trusted peer, recursive, multi hop skips trusted",
			trusted:   []string{"10.0.0.0/8"},
			recursive: true,
			peer:      "10.0.0.7:5000",
			hdrName:   "X-Forwarded-For",
			hdrVal:    "203.0.113.5, 10.0.0.8, 10.0.0.9",
			want:      "203.0.113.5",
		},
		{
			name:    "trusted peer, non-recursive takes rightmost",
			trusted: []string{"10.0.0.0/8"},
			peer:    "10.0.0.7:5000",
			hdrName: "X-Forwarded-For",
			hdrVal:  "203.0.113.5, 10.0.0.8",
			want:    "10.0.0.8",
		},
		{
			name:      "untrusted peer ignores header",
			trusted:   []string{"10.0.0.0/8"},
			recursive: true,
			peer:      "203.0.113.9:5000",
			hdrName:   "X-Forwarded-For",
			hdrVal:    "1.2.3.4",
			want:      "", // unchanged
		},
		{
			name:      "trusted peer but no header",
			trusted:   []string{"10.0.0.0/8"},
			recursive: true,
			peer:      "10.0.0.7:5000",
			want:      "",
		},
		{
			name:      "all entries trusted falls back to leftmost",
			trusted:   []string{"10.0.0.0/8"},
			recursive: true,
			peer:      "10.0.0.7:5000",
			hdrName:   "X-Forwarded-For",
			hdrVal:    "10.0.0.5, 10.0.0.8",
			want:      "10.0.0.5",
		},
		{
			name:    "X-Real-IP single value",
			trusted: []string{"10.0.0.0/8"},
			header:  "X-Real-IP",
			peer:    "10.0.0.7:5000",
			hdrName: "X-Real-IP",
			hdrVal:  "203.0.113.5",
			want:    "203.0.113.5",
		},
		{
			name:      "bare IP trusted_proxies entry",
			trusted:   []string{"10.0.0.7"},
			recursive: true,
			peer:      "10.0.0.7:5000",
			hdrName:   "X-Forwarded-For",
			hdrVal:    "203.0.113.5",
			want:      "203.0.113.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &realIPConfig{
				TrustedProxies: tt.trusted,
				Header:         tt.header,
				Recursive:      tt.recursive,
			}
			got := resolveWith(t, c, tt.peer, tt.hdrName, tt.hdrVal)
			if got != tt.want {
				t.Errorf("resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRealIPCompileErrors(t *testing.T) {
	t.Run("invalid CIDR", func(t *testing.T) {
		_, diags := compileRealIP(&realIPConfig{TrustedProxies: []string{"not-a-cidr"}})
		if !diags.HasErrors() {
			t.Fatal("expected error for invalid trusted_proxies entry")
		}
	})
	t.Run("empty trusted_proxies", func(t *testing.T) {
		_, diags := compileRealIP(&realIPConfig{TrustedProxies: nil})
		if !diags.HasErrors() {
			t.Fatal("expected error for empty trusted_proxies")
		}
	})
}

// realIPSource is a config with a real_ip block and a handle that echoes the
// resolved client address back through ctx.request.remote_addr.
const realIPSource = `
server "http" "main" {
  listen = "127.0.0.1:0"

  real_ip {
    trusted_proxies = ["10.0.0.0/8"]
    recursive       = true
  }

  handle "/ip" {
    action = ctx.request.remote_addr
  }
}
`

// TestRealIPEndToEnd confirms the middleware rewrites RemoteAddr so the value
// flows through to ctx.request.remote_addr in an action.
func TestRealIPEndToEnd(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources([]byte(realIPSource)).WithLogger(zap.NewNop()).Build()
	if diags.HasErrors() {
		t.Fatalf("build: %v", diags)
	}
	srv := c.Servers["http"]["main"].(*HttpServer)

	// Mirror Start()'s outermost wrapping (otelhttp is irrelevant to RemoteAddr).
	handler := srv.Server.Handler
	if srv.realIP != nil {
		handler = srv.realIP.wrap(handler)
	}

	get := func(peer, xff string) string {
		req := httptest.NewRequest(http.MethodGet, "/ip", nil)
		req.RemoteAddr = peer
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		body, _ := io.ReadAll(w.Result().Body)
		return string(body)
	}

	if got := get("10.0.0.7:5000", "203.0.113.5, 10.0.0.8"); got != "203.0.113.5" {
		t.Errorf("trusted peer: remote_addr = %q, want 203.0.113.5", got)
	}
	// Untrusted peer: header ignored, original RemoteAddr (with port) preserved.
	if got := get("203.0.113.9:5000", "1.2.3.4"); got != "203.0.113.9:5000" {
		t.Errorf("untrusted peer: remote_addr = %q, want 203.0.113.9:5000", got)
	}
}

// realIPDisabledSource mirrors how the traffic-light example env-toggles real_ip:
// disabled = true with an empty trusted_proxies list. The required-field check
// must be skipped so the config still builds, and no resolver is installed.
const realIPDisabledSource = `
server "http" "main" {
  listen = "127.0.0.1:0"

  real_ip {
    disabled        = true
    trusted_proxies = []
  }

  handle "/ip" {
    action = ctx.request.remote_addr
  }
}
`

// TestRealIPDisabled confirms a disabled real_ip block parses (despite empty
// trusted_proxies) and produces no resolver, so RemoteAddr is left untouched.
func TestRealIPDisabled(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources([]byte(realIPDisabledSource)).WithLogger(zap.NewNop()).Build()
	if diags.HasErrors() {
		t.Fatalf("build: %v", diags)
	}
	srv := c.Servers["http"]["main"].(*HttpServer)
	if srv.realIP != nil {
		t.Fatalf("expected no realIP resolver when disabled, got %#v", srv.realIP)
	}
}
