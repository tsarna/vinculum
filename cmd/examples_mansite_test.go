package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// TestManSiteAnswersOverMCP drives examples/man-site the way a client does, over
// the mounted /mcp route. TestExamplesAreValid only proves the config loads,
// and everything this example is for happens per request: the null-coalescing
// answers, the page-name guard in front of file(), the resource that has to
// answer an empty path before it looks anything up.
func TestManSiteAnswersOverMCP(t *testing.T) {
	handler, session := startManSite(t, nil)

	tool := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		return manSiteResult(t, manSitePost(t, handler, session, "tools/call",
			map[string]any{"name": name, "arguments": args}))
	}

	// The public posture: building submitted text is not offered, and not
	// advertised either.
	t.Run("without MAN_CHECK_PASSWORD there is no vcl_check", func(t *testing.T) {
		assert.NotContains(t, manSiteToolNames(t, handler, session), "vcl_check")
		assert.Contains(t, manSitePrompt(t, handler, session), "`vinculum check <file>`")
	})

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

	// The question an agent cannot answer from the language alone: how to run
	// what it wrote.
	t.Run("a command, and a function that needs one of its flags", func(t *testing.T) {
		got, isErr := tool("vcl_man", map[string]any{"topic": "vinculum serve"})
		assert.False(t, isErr, got)
		assert.Contains(t, got, "# `vinculum serve`")
		assert.Contains(t, got, "`-f, --file-path`")

		got, isErr = tool("vcl_man", map[string]any{"topic": "check", "kind": "command"})
		assert.False(t, isErr, got)
		assert.Contains(t, got, "# `vinculum check`")

		got, _ = tool("vcl_man", map[string]any{"topic": "templatefile"})
		assert.Contains(t, got, "Available only when run with `--file-path`")
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

// TestManSiteChecksWhenEnabled is the private posture: the same file, with
// MAN_CHECK_PASSWORD set and nothing edited.
func TestManSiteChecksWhenEnabled(t *testing.T) {
	handler, session := startManSite(t,
		map[string]string{"MAN_CHECK_PASSWORD": "s3cret"}, "check:s3cret")

	check := func(source string) string {
		t.Helper()
		got, isErr := manSiteResult(t, manSitePost(t, handler, session, "tools/call",
			map[string]any{"name": "vcl_check", "arguments": map[string]any{"config": source}},
			"check:s3cret"))
		assert.False(t, isErr, "an invalid config is an answer, not a failed call: %s", got)
		return got
	}

	assert.Contains(t, manSiteToolNames(t, handler, session, "check:s3cret"), "vcl_check")
	assert.Contains(t, manSitePrompt(t, handler, session, "check:s3cret"), "the vcl_check tool")

	got := check(`
bus "main" {}

subscription "s" {
  target = bus.main
  topics = ["a/#"]
  action = log::info(ctx.topic)
}
`)
	assert.True(t, strings.HasPrefix(got, "The configuration is valid.\n"), got)
	assert.Contains(t, got, "Checked by Vinculum")

	got = check("bus \"main\" {}\n\nclient \"mqtt\" \"m\" {\n  brokerz = [\"tcp://localhost:1883\"]\n}\n")
	assert.Contains(t, got, "The configuration is not valid")
	assert.Contains(t, got, "on config.vcl line 4")
	assert.NotContains(t, got, "mcp.vcl", "nothing about the server's own files")
}

// TestManSitePostures is the door, per posture. The tools are covered above;
// what matters here is who gets to reach them, and that no environment turns
// the checker on without also putting a password in front of it.
func TestManSitePostures(t *testing.T) {
	// initialize is the first call a client makes, so it is the one the door is
	// tested with. A 401 carries the realm, which says which password is wanted.
	initialize := func(t *testing.T, handler http.Handler, creds string) *httptest.ResponseRecorder {
		t.Helper()
		return manSiteTry(t, handler, "", "initialize", map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		}, creds)
	}

	t.Run("nothing set: anonymous on purpose", func(t *testing.T) {
		handler, _ := startManSite(t, nil)
		assert.Equal(t, http.StatusOK, initialize(t, handler, "").Code)
	})

	t.Run("MAN_PASSWORD closes the reference", func(t *testing.T) {
		handler, _ := startManSite(t, map[string]string{"MAN_PASSWORD": "site-pw"}, "docs:site-pw")

		w := initialize(t, handler, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Equal(t, `Basic realm="Vinculum reference"`, w.Header().Get("Www-Authenticate"))

		assert.Equal(t, http.StatusUnauthorized, initialize(t, handler, "docs:wrong").Code)
		assert.Equal(t, http.StatusOK, initialize(t, handler, "docs:site-pw").Code)
	})

	// The property the split posture is for: one variable both offers the
	// checker and demands a password for the route carrying it.
	t.Run("MAN_CHECK_PASSWORD closes the MCP route it opens", func(t *testing.T) {
		handler, _ := startManSite(t, map[string]string{"MAN_CHECK_PASSWORD": "check-pw"}, "check:check-pw")

		w := initialize(t, handler, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "the checker is never anonymous")
		assert.Equal(t, `Basic realm="Vinculum checker"`, w.Header().Get("Www-Authenticate"))
		assert.Equal(t, http.StatusOK, initialize(t, handler, "check:check-pw").Code)
	})

	t.Run("both set: the checker's password is the one the MCP route takes", func(t *testing.T) {
		handler, _ := startManSite(t, map[string]string{
			"MAN_PASSWORD": "site-pw", "MAN_CHECK_PASSWORD": "check-pw",
		}, "check:check-pw")

		assert.Equal(t, http.StatusUnauthorized, initialize(t, handler, "docs:site-pw").Code,
			"the route's own policy replaces the server's rather than adding to it")
		assert.Equal(t, http.StatusOK, initialize(t, handler, "check:check-pw").Code)
	})

	t.Run("the usernames are configurable", func(t *testing.T) {
		handler, _ := startManSite(t, map[string]string{
			"MAN_CHECK_PASSWORD": "check-pw", "MAN_CHECK_USER": "agent",
		}, "agent:check-pw")

		assert.Equal(t, http.StatusUnauthorized, initialize(t, handler, "check:check-pw").Code)
		assert.Equal(t, http.StatusOK, initialize(t, handler, "agent:check-pw").Code)
	})
}

// TestManCheckWithEveryBuiltin covers what internal/schemadoc's own tests
// cannot, because that package links no function plugins: the functions a
// checked config calls behave as documented.
func TestManCheckWithEveryBuiltin(t *testing.T) {
	check := func(t *testing.T, source string) map[string]cty.Value {
		t.Helper()
		src := fmt.Sprintf("const {\n  r = man::check(%q)\n}\n", source)
		cfg, diags := config.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), "%s", diags)
		t.Cleanup(cfg.Discard)
		return cfg.Constants["r"].AsValueMap()
	}

	t.Run("try() falls back on the empty environment", func(t *testing.T) {
		t.Setenv("VINCULUM_CHECK_SECRET", "hunter2")
		r := check(t, `const { x = try(env.VINCULUM_CHECK_SECRET, "default") }`)
		assert.True(t, r["valid"].True(), r["text"].AsString())

		r = check(t, `const { x = tonumber(env.VINCULUM_CHECK_SECRET) }`)
		assert.False(t, r["valid"].True())
		assert.NotContains(t, r["text"].AsString(), "hunter2")
	})

	// They exist, so a config that serves files checks, but they are rooted at
	// an empty directory.
	t.Run("file functions exist and read nothing real", func(t *testing.T) {
		r := check(t, `const { x = fileexists("go.mod") }`)
		require.True(t, r["valid"].True(), r["text"].AsString())

		r = check(t, `const { x = file("examples_mansite_test.go") }`)
		assert.False(t, r["valid"].True())
		assert.NotContains(t, r["text"].AsString(), os.TempDir(), "the scratch directory is not named")
	})

	// The inner check finds the slot taken and is refused within a second — a
	// value in the outer config, not a problem with it — rather than waiting out
	// the outer check's whole timeout on a slot the outer check holds.
	t.Run("a config that calls man::check itself does not deadlock", func(t *testing.T) {
		start := time.Now()
		r := check(t, `const { inner = man::check("bus \"main\" {}") }`)
		assert.True(t, r["valid"].True(), r["text"].AsString())
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}

// startManSite builds examples/man-site with env set and returns its HTTP
// handler and an MCP session initialized with creds ("user:password", or
// nothing for an anonymous one). Build starts nothing: the handler is driven
// directly, with no listener.
func startManSite(t *testing.T, env map[string]string, creds ...string) (http.Handler, string) {
	t.Helper()

	// The layout the README documents: --file-path at a checkout, pages in doc/.
	// The example reads these variables; one set in a developer's shell must not
	// change what this checks. env.* holds only variables that are set, so they
	// are unset rather than emptied. t.Setenv first registers the restore — and
	// makes the test refuse t.Parallel, which a process-wide unset could not
	// survive.
	for _, name := range []string{
		"MAN_DOC_DIR", "MAN_LISTEN", "MAN_CHECK_PASSWORD", "MAN_CHECK_USER",
		"MAN_PASSWORD", "MAN_USER", "MAN_DOC_FETCH", "MAN_DOC_TAG", "MAN_DOC_REPO", "MAN_DOC_INTO",
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	for k, v := range env {
		t.Setenv(k, v)
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
	t.Cleanup(cfg.Discard)

	handler := cfg.Servers["http"]["main"].(*httpserver.HttpServer).Server.Handler
	return handler, manSiteInitialize(t, handler, creds...)
}

// manSiteToolNames is the names tools/list advertises.
func manSiteToolNames(t *testing.T, handler http.Handler, session string, creds ...string) []string {
	t.Helper()
	tools, ok := manSiteMessage(t, manSitePost(t, handler, session, "tools/list", map[string]any{}, creds...))["tools"].([]any)
	require.True(t, ok)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	return names
}

// manSitePrompt is the text of the write_vcl prompt.
func manSitePrompt(t *testing.T, handler http.Handler, session string, creds ...string) string {
	t.Helper()
	msgs, ok := manSiteMessage(t, manSitePost(t, handler, session, "prompts/get",
		map[string]any{"name": "write_vcl"}, creds...))["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, msgs)
	return msgs[0].(map[string]any)["content"].(map[string]any)["text"].(string)
}

func manSiteInitialize(t *testing.T, handler http.Handler, creds ...string) string {
	t.Helper()
	w := manSitePost(t, handler, "", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	}, creds...)
	session := w.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, session, "initialize: %s", w.Body.String())
	return session
}

func manSitePost(t *testing.T, handler http.Handler, session, method string, params any, creds ...string) *httptest.ResponseRecorder {
	t.Helper()
	w := manSiteTry(t, handler, session, method, params, creds...)
	require.Equal(t, http.StatusOK, w.Code, "%s: %s", method, w.Body.String())
	return w
}

// manSiteTry posts without insisting on a status, for the cases that are about
// the status. creds is "user:password", or nothing for an anonymous request.
func manSiteTry(t *testing.T, handler http.Handler, session, method string, params any, creds ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if len(creds) > 0 && creds[0] != "" {
		user, password, _ := strings.Cut(creds[0], ":")
		req.SetBasicAuth(user, password)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
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
