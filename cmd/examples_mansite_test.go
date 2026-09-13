package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

// TestManSiteAnswersOverMCP drives examples/man-site the way a client does, over
// the mounted /mcp route. TestExamplesAreValid only proves the config loads,
// and everything this example is for happens per request: the null-coalescing
// answers, the page-name guard in front of file(), the resource that has to
// answer an empty path before it looks anything up.
func TestManSiteAnswersOverMCP(t *testing.T) {
	// The layout the README documents: --file-path at a checkout, pages in doc/.
	// The example reads MAN_DOC_DIR and MAN_LISTEN; one set in a developer's
	// shell must not change what this checks. env.* holds only variables that
	// are set, so they are unset rather than emptied. t.Setenv first registers
	// the restore — and makes the test refuse t.Parallel, which a
	// process-wide unset could not survive.
	for _, name := range []string{"MAN_DOC_DIR", "MAN_LISTEN"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}

	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "doc"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(base, "doc", "functy.md"), []byte("# functy, from the seed\n"), 0o600))

	cfg, diags := config.NewConfig().
		WithSources(filepath.Join("..", "examples", "man-site")).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", base).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	t.Cleanup(func() {
		// Teardown in the order TestExamplesAreValid uses, though Build starts
		// nothing here: the handler is driven directly, with no listener.
		drain(cfg, zap.NewNop(), config.DefaultShutdownTimeout)
		for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
			cfg.Stoppables[i].Stop() //nolint:errcheck
		}
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	})

	handler := cfg.Servers["http"]["main"].(*httpserver.HttpServer).Server.Handler
	session := manSiteInitialize(t, handler)

	tool := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		return manSiteResult(t, manSitePost(t, handler, session, "tools/call",
			map[string]any{"name": name, "arguments": args}))
	}

	t.Run("a page is read from the documented path", func(t *testing.T) {
		got, isErr := tool("vcl_doc", map[string]any{"page": "functy"})
		assert.False(t, isErr)
		assert.Equal(t, "# functy, from the seed\n", got)

		got, _ = tool("vcl_doc", map[string]any{"page": "functy.md"})
		assert.Equal(t, "# functy, from the seed\n", got, "the extension a caller types is allowed")
	})

	t.Run("a name that is not a page never reaches file()", func(t *testing.T) {
		for _, page := range []string{"../../etc/passwd", "func.mdty", "%2e%2e", "Functy", ""} {
			got, _ := tool("vcl_doc", map[string]any{"page": page})
			assert.Contains(t, got, "is not a page name", page)
			assert.Contains(t, got, "  - functy", "and the answer lists the pages there are")
		}
	})

	t.Run("a missing page lists the pages", func(t *testing.T) {
		got, isErr := tool("vcl_doc", map[string]any{"page": "nope"})
		assert.False(t, isErr)
		assert.Contains(t, got, `There is no page named "nope"`)
		assert.Contains(t, got, "  - functy")
	})

	t.Run("a miss is text, not an error", func(t *testing.T) {
		got, isErr := tool("vcl_man", map[string]any{"topic": "nope_nope"})
		assert.False(t, isErr, "a lookup that found nothing succeeded")
		assert.Contains(t, got, `No topic in the Vinculum reference is named "nope_nope"`)

		got, isErr = tool("vcl_apropos", map[string]any{"keywords": "zzzznope"})
		assert.False(t, isErr)
		assert.Contains(t, got, `Nothing in the Vinculum reference matches "zzzznope"`)
	})

	// man:: calls these errors, and an action error's text begins with the
	// config file's absolute path — not something a public endpoint should
	// hand to whoever asks.
	t.Run("a malformed topic is a miss, and says nothing about the server", func(t *testing.T) {
		for _, topic := range []string{"", "   ", ":", "function:", "blok:x"} {
			for _, name := range []string{"vcl_man", "vcl_synopsis"} {
				got, isErr := tool(name, map[string]any{"topic": topic})
				assert.False(t, isErr, "%s %q: %s", name, topic, got)
				assert.Contains(t, got, "No topic in the Vinculum reference", "%s %q", name, topic)
				assert.NotContains(t, got, "mcp.vcl", "%s %q", name, topic)
			}
		}

		got, isErr := tool("vcl_apropos", map[string]any{"keywords": "   "})
		assert.False(t, isErr, got)
		assert.NotContains(t, got, "mcp.vcl")
	})

	t.Run("an ambiguous name is the menu of topic paths", func(t *testing.T) {
		got, _ := tool("vcl_man", map[string]any{"topic": "http"})
		assert.Contains(t, got, "    client http\n")

		got, _ = tool("vcl_synopsis", map[string]any{"topic": "client"})
		assert.Contains(t, got, `"client" takes a`)
		assert.Contains(t, got, "    client mqtt\n")
	})

	t.Run("the kind enum is enforced before the action runs", func(t *testing.T) {
		got, isErr := tool("vcl_man", map[string]any{"topic": "assert", "kind": "bogus"})
		assert.True(t, isErr)
		assert.Contains(t, got, `"kind" must be one of`)

		got, isErr = tool("vcl_man", map[string]any{"topic": "assert", "kind": "function"})
		assert.False(t, isErr)
		assert.Contains(t, got, "# `assert()`")

		// A null kind gets its default rather than reaching the action as a
		// null, which the helper function it is passed to would reject.
		got, isErr = tool("vcl_man", map[string]any{"topic": "subscription", "kind": nil})
		assert.False(t, isErr, got)
		assert.Contains(t, got, "# `subscription`")
	})

	t.Run("an empty or malformed topic path is answered, not an error", func(t *testing.T) {
		w := manSitePost(t, handler, session, "resources/read", map[string]any{"uri": "vcl://topic/"})
		assert.Contains(t, manSiteContents(t, w), "Give a topic path")

		// A misspelled kind is an error to man::page, and a resource has no
		// error content, so it would otherwise be a raw protocol error.
		w = manSitePost(t, handler, session, "resources/read", map[string]any{"uri": "vcl://topic/blok:x"})
		assert.Contains(t, manSiteContents(t, w), "No topic in the Vinculum reference is named")

		w = manSitePost(t, handler, session, "resources/read", map[string]any{"uri": "vcl://topic/client/mqtt"})
		assert.Contains(t, manSiteContents(t, w), "# `client \"mqtt\"`")
	})
}

func manSiteInitialize(t *testing.T, handler http.Handler) string {
	t.Helper()
	w := manSitePost(t, handler, "", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	session := w.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, session, "initialize: %s", w.Body.String())
	return session
}

func manSitePost(t *testing.T, handler http.Handler, session, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "%s: %s", method, w.Body.String())
	return w
}

// manSiteMessage decodes the JSON-RPC result from a Streamable HTTP answer,
// which arrives as SSE with the JSON on a "data:" line.
func manSiteMessage(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	for _, line := range strings.Split(w.Body.String(), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var msg map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &msg) == nil {
			result, ok := msg["result"].(map[string]any)
			require.True(t, ok, "no result: %s", w.Body.String())
			return result
		}
	}
	t.Fatalf("no JSON data line: %s", w.Body.String())
	return nil
}

// manSiteResult is a tools/call answer's text and whether it is a tool error.
func manSiteResult(t *testing.T, w *httptest.ResponseRecorder) (string, bool) {
	t.Helper()
	result := manSiteMessage(t, w)
	content, ok := result["content"].([]any)
	require.True(t, ok, "no content: %s", w.Body.String())
	require.Len(t, content, 1)
	isErr, _ := result["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}

// manSiteContents is a resources/read answer's text.
func manSiteContents(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	contents, ok := manSiteMessage(t, w)["contents"].([]any)
	require.True(t, ok, "no contents: %s", w.Body.String())
	require.Len(t, contents, 1)
	return contents[0].(map[string]any)["text"].(string)
}
