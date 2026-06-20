package httpserver

import "testing"

func TestSplitPattern(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		wantMethod string
		wantHost   string
		wantPath   string
		wantOK     bool
	}{
		{"host-less path", "/health", "", "", "/health", true},
		{"method only", "GET /health", "GET", "", "/health", true},
		{"host only", "api.example.com/v1/items/{id}", "", "api.example.com", "/v1/items/{id}", true},
		{"method and host", "GET api.example.com/v1/items/{id}", "GET", "api.example.com", "/v1/items/{id}", true},
		{"subtree host", "cdn.example.com/static/", "", "cdn.example.com", "/static/", true},
		{"leading and trailing spaces", "  GET api.example.com/x  ", "GET", "api.example.com", "/x", true},
		{"extra spaces after method", "GET   /x", "GET", "", "/x", true},
		{"no slash", "example.com", "", "", "", false},
		{"method but no slash", "GET example.com", "GET", "", "", false},
		{"empty", "", "", "", "", false},
		{"root path", "/", "", "", "/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, host, path, ok := splitPattern(tt.pattern)
			if method != tt.wantMethod || host != tt.wantHost || path != tt.wantPath || ok != tt.wantOK {
				t.Errorf("splitPattern(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					tt.pattern, method, host, path, ok,
					tt.wantMethod, tt.wantHost, tt.wantPath, tt.wantOK)
			}
		})
	}
}
