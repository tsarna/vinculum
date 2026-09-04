package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// The examples are the first configuration most people read, and nothing was
// checking that they still load. This release alone made three changes that
// would have broken one silently — `bus.main` stopped being implicit, `target`
// became required on `subscription`, and `bus` became required on
// `server "vws"` and `server "websocket"` — and an example that no longer
// parses teaches the language wrong.
//
// This is `vinculum check` on each of them, as a test rather than a CI step so
// that it runs before a push as well as after one. It is the same path: build
// the config, tear it down, and assert nothing was reported. Building starts
// nothing and binds no port, so there is no listener here to collide with a
// parallel test or a busy CI runner.
//
// Living in cmd is what gives it every block type: this package imports the
// whole binary's registrations, so an example using any client, server or
// trigger resolves the same way it does in production.

// exampleCase is one example directory and the environment it documents as
// required. Anything optional is deliberately left unset, so the test also
// covers each `try(env.X, default)` falling back.
type exampleCase struct {
	dir string

	// env is set for the duration of the case.
	env map[string]string

	// filePath enables the file functions, as `--file-path` does. Relative to
	// the example directory; "." is the example directory itself.
	filePath string

	// seed are files written into a temp directory that then becomes the
	// file/write path, for an example that reads something it does not ship.
	// Mutually exclusive with filePath.
	seed map[string]string

	// writePath enables the file *write* functions on the same directory, as
	// `--write-path` does — an `editor` in its default file mode needs it.
	writePath bool
}

func TestExamplesAreValid(t *testing.T) {
	cases := []exampleCase{{
		dir: "weather-mcp",
	}, {
		dir: "voipms",
		// The only two the README lists as required; everything else falls back.
		env: map[string]string{
			"VOIPMS_API_USER":     "user",
			"VOIPMS_API_PASSWORD": "password",
		},
	}, {
		dir: "traffic-light",
		// The `files` block serves the shipped html/ directory.
		filePath: ".",
		env:      map[string]string{"TRAFFIC_HTML_DIR": "html"},
	}, {
		dir: "dns-zone-updater",
		// Reads a credentials file it does not ship, and writes zone files
		// through an `editor "line"` in file mode. The fixture is the shape the
		// README documents, so the `auth` block it feeds is really exercised.
		seed:      map[string]string{"dns-updaters.json": `{"dyn.example.com/foo":"s3cret"}`},
		writePath: true,
	}}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			dir := filepath.Join("..", "examples", tc.dir)
			require.DirExists(t, dir, "the example directory named by this case must exist")

			builder := config.NewConfig().
				WithSources(dir).
				WithLogger(zap.NewNop())

			if base := featureBase(t, dir, tc); base != "" {
				builder = builder.WithFeature("readfiles", base)
				if tc.writePath {
					builder = builder.WithFeature("writefiles", base)
				}
			}

			cfg, diags := builder.Build()
			if cfg != nil {
				// Teardown in the order `vinculum check` uses, so a Drainable
				// that acquires something at construction still gets released.
				drain(cfg, zap.NewNop())
				for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
					cfg.Stoppables[i].Stop() //nolint:errcheck
				}
				for _, b := range cfg.Buses {
					b.Stop() //nolint:errcheck
				}
			}

			require.False(t, diags.HasErrors(),
				"examples/%s no longer loads:\n%s", tc.dir, diags.Error())
		})
	}
}

// featureBase returns the directory the file functions should be rooted at, or
// "" when this example does not use them.
func featureBase(t *testing.T, dir string, tc exampleCase) string {
	t.Helper()

	switch {
	case tc.filePath != "":
		return filepath.Join(dir, tc.filePath)

	case len(tc.seed) > 0:
		base := t.TempDir()
		for name, content := range tc.seed {
			require.NoError(t, os.WriteFile(filepath.Join(base, name), []byte(content), 0o600))
		}
		return base

	case tc.writePath:
		return t.TempDir()
	}
	return ""
}

// TestExamplesAreAllCovered keeps the table above honest: a new example
// directory that nobody added a case for would otherwise be unchecked, which is
// exactly the gap this file exists to close.
func TestExamplesAreAllCovered(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "examples"))
	require.NoError(t, err)

	covered := map[string]bool{
		"weather-mcp": true, "voipms": true,
		"traffic-light": true, "dns-zone-updater": true,
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		require.True(t, covered[e.Name()],
			"examples/%s has no case in TestExamplesAreValid; add one so it is checked", e.Name())
	}
}
