package config_test

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// buildVinit writes src to a .vinit file and runs Build, returning the diags.
func buildVinit(t *testing.T, src string) hcl.Diagnostics {
	t.Helper()
	dir := writeVinit(t, "boot", src)
	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	return diags
}

func TestGit_StaticValidation(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantError string // substring; "" means expect success
	}{
		{
			name: "missing repo (empty interpolation)",
			src: `
git "x" {
    repo = ""
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "requires repo",
		},
		{
			name: "branch and tag both set",
			src: `
git "x" {
    repo   = "https://example.com/r.git"
    branch = "main"
    tag    = "v1"
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "Conflicting git revision",
		},
		{
			name: "branch tag and commit all set",
			src: `
git "x" {
    repo   = "https://example.com/r.git"
    branch = "main"
    tag    = "v1"
    commit = "deadbeef"
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "Conflicting git revision",
		},
		{
			name: "no fetch blocks",
			src: `
git "x" {
    repo = "https://example.com/r.git"
}`,
			wantError: "requires a fetch",
		},
		{
			name: "fetch missing into (empty)",
			src: `
git "x" {
    repo = "https://example.com/r.git"
    fetch "a" { into = "" }
}`,
			wantError: "requires into",
		},
		{
			name: "from with .. escape",
			src: `
git "x" {
    repo = "https://example.com/r.git"
    fetch "a" {
        from = "../etc"
        into = "/tmp/x"
    }
}`,
			wantError: "escapes the repository root",
		},
		{
			name: "from with leading slash",
			src: `
git "x" {
    repo = "https://example.com/r.git"
    fetch "a" {
        from = "/etc"
        into = "/tmp/x"
    }
}`,
			wantError: "must be repo-relative",
		},
		{
			name: "token on ssh repo",
			src: `
git "x" {
    repo = "git@github.com:org/r.git"
    auth { token = "abc" }
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "does not match transport",
		},
		{
			name: "private_key on https repo",
			src: `
git "x" {
    repo = "https://example.com/r.git"
    auth { private_key = "KEY" }
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "does not match transport",
		},
		{
			name: "token plus username",
			src: `
git "x" {
    repo = "https://example.com/r.git"
    auth {
        token    = "abc"
        username = "u"
    }
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "mutually exclusive",
		},
		{
			name: "two ssh keys",
			src: `
git "x" {
    repo = "ssh://git@example.com/r.git"
    auth {
        private_key      = "K"
        private_key_file = "/k"
    }
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "mutually exclusive",
		},
		{
			name: "known_hosts plus insecure",
			src: `
git "x" {
    repo = "ssh://git@example.com/r.git"
    auth {
        known_hosts              = "/kh"
        insecure_ignore_host_key = true
    }
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "mutually exclusive",
		},
		{
			name: "disabled block short-circuits (no clone attempted)",
			src: `
git "x" {
    disabled = true
    repo     = "https://example.com/does-not-resolve.git"
    fetch "a" { into = "/tmp/x" }
}`,
			wantError: "",
		},
		{
			name: "valid block passes validation and reaches clone",
			// A local path that does not exist: validation passes, so the
			// failure comes from the clone stage (not the network).
			src: `
git "x" {
    repo = "/nonexistent/vinculum/git/repo"
    fetch "a" {
        from = "config"
        into = "/tmp/x"
    }
}`,
			wantError: "git clone failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := buildVinit(t, tc.src)
			if tc.wantError == "" {
				require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
				return
			}
			require.True(t, diags.HasErrors(), "expected error containing %q", tc.wantError)
			assert.Contains(t, diags.Error(), tc.wantError)
		})
	}
}

func TestGit_DuplicateLabelFatal(t *testing.T) {
	diags := buildVinit(t, `
git "dup" {
    disabled = true
    repo     = "x"
    fetch "a" { into = "/tmp/x" }
}
git "dup" {
    disabled = true
    repo     = "x"
    fetch "a" { into = "/tmp/x" }
}
`)
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Duplicate git label")
}
