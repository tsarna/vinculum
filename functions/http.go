package functions

import (
	"bytes"
	"context"
	"fmt"
	"io"
	nethttp "net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"time"

	clientshttp "github.com/tsarna/vinculum/clients/http"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func init() {
	cfg.RegisterFunctionPlugin("http", func(_ *cfg.Config) map[string]function.Function {
		return GetHTTPClientFunctions()
	})
}

// GetHTTPClientFunctions returns the eight http_* verb functions.
func GetHTTPClientFunctions() map[string]function.Function {
	return map[string]function.Function{
		"http_get":     makeVerbFunc("GET", false),
		"http_head":    makeVerbFunc("HEAD", false),
		"http_options": makeVerbFunc("OPTIONS", false),
		"http_delete":  makeVerbFunc("DELETE", false),
		"http_post":    makeVerbFunc("POST", true),
		"http_put":     makeVerbFunc("PUT", true),
		"http_patch":   makeVerbFunc("PATCH", true),
		"http_request": makeGenericVerbFunc(),
	}
}

// ─── Verb function constructors ──────────────────────────────────────────────

// makeVerbFunc builds a verb function for a fixed HTTP method. Body-bearing
// verbs accept (ctx, client, url, body?, opts?); body-less verbs accept
// (ctx, client, url, opts?). The trailing optional slots are resolved by
// argument count at call time.
func makeVerbFunc(method string, bodyAllowed bool) function.Function {
	return function.New(&function.Spec{
		Description: fmt.Sprintf("Performs an HTTP %s request and returns the response object", method),
		Params: []function.Parameter{
			{Name: "ctx", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
			{Name: "client", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
			{Name: "url", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
		},
		VarParam: &function.Parameter{Name: "rest", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
		Type:     function.StaticReturnType(types.HTTPClientResponseObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, client, rawURL, body, opts, err := parseVerbArgs(args, method, bodyAllowed)
			if err != nil {
				return cty.NilVal, err
			}
			return doHTTPRequest(ctx, client, method, rawURL, body, opts)
		},
	})
}

// makeGenericVerbFunc builds the generic http_request(ctx, client, method,
// url, body?, opts?) function.
func makeGenericVerbFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Performs a generic HTTP request with an explicit method and returns the response object",
		Params: []function.Parameter{
			{Name: "ctx", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
			{Name: "client", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
			{Name: "method", Type: cty.String},
			{Name: "url", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
		},
		VarParam: &function.Parameter{Name: "rest", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true},
		Type:     function.StaticReturnType(types.HTTPClientResponseObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			method := strings.ToUpper(args[2].AsString())
			// Rebuild arg list as [ctx, client, url, rest...] so we can
			// reuse parseVerbArgs. Body is always allowed for the generic
			// form (the caller chose the method).
			rebuilt := make([]cty.Value, 0, len(args)-1)
			rebuilt = append(rebuilt, args[0], args[1], args[3])
			rebuilt = append(rebuilt, args[4:]...)
			ctx, client, rawURL, body, opts, err := parseVerbArgs(rebuilt, method, true)
			if err != nil {
				return cty.NilVal, err
			}
			return doHTTPRequest(ctx, client, method, rawURL, body, opts)
		},
	})
}

// parseVerbArgs deconstructs a verb function's argument list into its
// semantic parts. For body-bearing verbs, the trailing args are [body, opts?];
// for body-less verbs, the trailing args are [opts?]. A non-null body on a
// body-less verb is a user error per the spec.
func parseVerbArgs(args []cty.Value, method string, bodyAllowed bool) (
	context.Context, clientshttp.HTTPCallable, cty.Value, cty.Value, cty.Value, error,
) {
	if len(args) < 3 {
		return nil, nil, cty.NilVal, cty.NilVal, cty.NilVal,
			fmt.Errorf("http_%s: at least 3 arguments required (ctx, client, url)", strings.ToLower(method))
	}
	ctx, err := ctyutil.GetContextFromValue(args[0])
	if err != nil {
		return nil, nil, cty.NilVal, cty.NilVal, cty.NilVal,
			fmt.Errorf("http_%s: invalid ctx: %w", strings.ToLower(method), err)
	}

	client, err := clientshttp.GetHTTPCallableFromValue(args[1])
	if err != nil {
		return nil, nil, cty.NilVal, cty.NilVal, cty.NilVal,
			fmt.Errorf("http_%s: invalid client: %w", strings.ToLower(method), err)
	}
	if client == nil {
		client = clientshttp.NullClient()
	}

	rawURL := args[2]
	body := cty.NullVal(cty.DynamicPseudoType)
	opts := cty.NullVal(cty.DynamicPseudoType)

	rest := args[3:]
	if bodyAllowed {
		switch len(rest) {
		case 0:
		case 1:
			body = rest[0]
		case 2:
			body = rest[0]
			opts = rest[1]
		default:
			return nil, nil, cty.NilVal, cty.NilVal, cty.NilVal,
				fmt.Errorf("http_%s: too many arguments; expected at most (ctx, client, url, body, opts)", strings.ToLower(method))
		}
	} else {
		switch len(rest) {
		case 0:
		case 1:
			opts = rest[0]
		default:
			return nil, nil, cty.NilVal, cty.NilVal, cty.NilVal,
				fmt.Errorf("http_%s: too many arguments; expected at most (ctx, client, url, opts)", strings.ToLower(method))
		}
	}

	return ctx, client, rawURL, body, opts, nil
}

// ─── Core request dispatch ───────────────────────────────────────────────────

// doHTTPRequest builds, sends, and materializes a single HTTP request. It
// handles URL resolution, header precedence, body coercion, query params,
// per-call redirect and timeout overrides, body_limit, and opts.as pre-decoding.
func doHTTPRequest(
	ctx context.Context,
	client clientshttp.HTTPCallable,
	method string,
	rawURL cty.Value,
	body cty.Value,
	opts cty.Value,
) (cty.Value, error) {
	verbName := "http_" + strings.ToLower(method)

	// ── URL resolution ──
	reqURL, err := resolveURL(client.BaseURL(), rawURL)
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s: %w", verbName, err)
	}

	// ── Body coercion ──
	var bodyBytes []byte
	var bodyContentType string
	isBytesBody := false
	if !body.IsNull() {
		// Detect bytes body before coercion so we can route Content-Type
		// through the right precedence level.
		if _, bErr := types.GetBytesFromValue(body); bErr == nil {
			isBytesBody = true
		}
		data, ct, cErr := types.CoerceBodyToBytes(body)
		if cErr != nil {
			return cty.NilVal, fmt.Errorf("%s: body: %w", verbName, cErr)
		}
		bodyBytes = data
		bodyContentType = ct
	}

	// ── Options ──
	parsedOpts, err := parseOpts(opts)
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s: opts: %w", verbName, err)
	}

	// ── Query parameters from opts.query ──
	if len(parsedOpts.query) > 0 {
		q := reqURL.Query()
		// Sort keys for deterministic ordering in tests.
		keys := make([]string, 0, len(parsedOpts.query))
		for k := range parsedOpts.query {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range parsedOpts.query[k] {
				q.Add(k, v)
			}
		}
		reqURL.RawQuery = q.Encode()
	}

	// ── Build headers per precedence chain ──
	hdrs := buildRequestHeaders(client, bodyContentType, isBytesBody, parsedOpts.headers)

	// ── Build the *http.Request ──
	var bodyReader io.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := nethttp.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s: build request: %w", verbName, err)
	}
	req.Header = hdrs
	if len(bodyBytes) > 0 {
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	// ── Per-call timeout override ──
	// Enforced via context deadline; Go's http.Client honors whichever of
	// its own Timeout and the request context fires first.
	if parsedOpts.hasTimeout {
		timedCtx, cancel := context.WithTimeout(req.Context(), parsedOpts.timeout)
		defer cancel()
		req = req.WithContext(timedCtx)
	}

	// Record the originally-sent URL for redirect detection.
	origURL := *reqURL

	// ── Send ──
	// Per-call redirect overrides go through DoWithRedirectPolicy, which
	// builds a fresh *http.Client with a different CheckRedirect closure but
	// reuses the same underlying Transport (and connection pool). When no
	// override is set, the common Do path uses the client's installed
	// CheckRedirect directly.
	var resp *nethttp.Response
	if parsedOpts.hasRedirectOverride {
		pol := client.DefaultRedirectPolicy()
		pol.Follow = parsedOpts.followRedirects
		pol.Max = parsedOpts.maxRedirects
		resp, err = client.DoWithRedirectPolicy(req, pol)
	} else {
		resp, err = client.Do(req)
	}
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s: %w", verbName, err)
	}

	// Always buffer the response body up-front and close the live stream.
	// The spec's Phase 2 design explicitly buffers every response body into
	// memory; streaming is future work. Buffering here lets later get()
	// calls re-read the body arbitrarily, and avoids lifetime issues with
	// the caller holding the wrapper after this function returns.
	data, readErr := readResponseBody(resp.Body, parsedOpts.bodyLimit)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return cty.NilVal, fmt.Errorf("%s: read body: %w", verbName, readErr)
	}
	if closeErr != nil {
		return cty.NilVal, fmt.Errorf("%s: close body: %w", verbName, closeErr)
	}
	// Replace the live stream with a re-readable buffer so get(r, "body")
	// still works and returns the buffered bytes.
	resp.Body = io.NopCloser(bytes.NewReader(data))

	wrapper := &types.HTTPClientResponseWrapper{
		R:            resp,
		Redirected:   resp.Request != nil && resp.Request.URL != nil && !sameURL(&origURL, resp.Request.URL),
		BufferedBody: data,
	}

	// ── opts.as pre-decode ──
	switch parsedOpts.as {
	case "none":
		// No pre-decode; the buffered body is still available via get().
	case "string":
		wrapper.PreDecodedBody = cty.StringVal(string(data))
	case "bytes":
		wrapper.PreDecodedBody = types.BuildBytesObject(data, stripMIMEParams(resp.Header.Get("Content-Type")))
	case "json":
		ty, jErr := ctyjson.ImpliedType(data)
		if jErr != nil {
			return cty.NilVal, fmt.Errorf("%s: decode body as json: %w", verbName, jErr)
		}
		val, jErr := ctyjson.Unmarshal(data, ty)
		if jErr != nil {
			return cty.NilVal, fmt.Errorf("%s: decode body as json: %w", verbName, jErr)
		}
		wrapper.PreDecodedBody = val
	}

	return types.BuildHTTPClientResponseObject(wrapper), nil
}

// readResponseBody reads up to limit bytes from r, or everything if limit <= 0.
func readResponseBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	return io.ReadAll(io.LimitReader(r, limit))
}

func sameURL(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Scheme == b.Scheme &&
		a.Host == b.Host &&
		a.Path == b.Path &&
		a.RawQuery == b.RawQuery &&
		a.Fragment == b.Fragment
}

func stripMIMEParams(ct string) string {
	if i := strings.Index(ct, ";"); i >= 0 {
		return strings.TrimSpace(ct[:i])
	}
	return strings.TrimSpace(ct)
}

// ─── URL resolution ──────────────────────────────────────────────────────────

// resolveURL converts a cty url argument into a concrete *url.URL, resolving
// relative URLs against the client's base_url.
//
// Query parameters present on the base URL are merged into the result so that
// "shared" params like API keys can be set once on the client. RFC 3986
// ResolveReference would otherwise drop them whenever the call's URL has its
// own query string. Per-call query params win on key collision; per-call keys
// not present in base are added.
func resolveURL(base *url.URL, rawURL cty.Value) (*url.URL, error) {
	if rawURL.IsNull() {
		return nil, fmt.Errorf("url: null not allowed")
	}
	u, err := types.GetURLFromValue(rawURL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}

	var resolved *url.URL
	if u.IsAbs() || base == nil {
		// Return a copy so callers can mutate query params safely without
		// affecting the original.
		cp := *u
		resolved = &cp
	} else {
		resolved = base.ResolveReference(u)
	}

	// Merge base query params into the resolved URL. Skip when base has no
	// query, when base is nil (already-absolute call URL with no base), or
	// when the call's URL is absolute (base is intentionally bypassed).
	if base != nil && base.RawQuery != "" && !u.IsAbs() {
		mergeBaseQuery(resolved, base)
	}

	return resolved, nil
}

// mergeBaseQuery copies query parameters from base into resolved. For each
// key on base, the values are appended to resolved's existing values for
// that key — except when resolved already has at least one value for that
// key, in which case the call's values win and the base's are dropped.
func mergeBaseQuery(resolved, base *url.URL) {
	baseQuery := base.Query()
	if len(baseQuery) == 0 {
		return
	}
	resolvedQuery := resolved.Query()
	for k, vs := range baseQuery {
		if _, exists := resolvedQuery[k]; exists {
			// Call's values win; do not append base values for this key.
			continue
		}
		resolvedQuery[k] = append([]string(nil), vs...)
	}
	resolved.RawQuery = resolvedQuery.Encode()
}

// ─── Header precedence assembly ──────────────────────────────────────────────

// buildRequestHeaders assembles the final request headers by walking the
// precedence chain (from weakest to strongest):
//
//  1. Built-in defaults (User-Agent, Accept). For non-bytes bodies, the
//     body's derived Content-Type is also a level-1 default.
//  2. Client default headers.
//  3. (Reserved for Phase 4 auth and Phase 6 OTel propagation.)
//  4. Content-Type from a bytes body — stronger than client defaults.
//  5. Per-call opts.headers — highest; a nil slice at this level deletes.
func buildRequestHeaders(
	client clientshttp.HTTPCallable,
	bodyContentType string,
	isBytesBody bool,
	perCall nethttp.Header,
) nethttp.Header {
	h := make(nethttp.Header)

	// Level 1: built-in defaults.
	h.Set("User-Agent", "vinculum")
	h.Set("Accept", "*/*")
	if bodyContentType != "" && !isBytesBody {
		h.Set("Content-Type", bodyContentType)
	}

	// Level 2: client default headers. Override per-name.
	for name, vals := range client.DefaultHeaders() {
		h[name] = append([]string(nil), vals...)
	}

	// Level 3: reserved for Phase 4 (auth) and Phase 6 (OTel).

	// Level 4: bytes body Content-Type. Strictly stronger than client config.
	if isBytesBody && bodyContentType != "" {
		h.Set("Content-Type", bodyContentType)
	}

	// Level 5: per-call opts.headers. A nil slice means delete.
	for name, vals := range perCall {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if vals == nil {
			h.Del(canonical)
			continue
		}
		h.Del(canonical)
		for _, v := range vals {
			h.Add(canonical, v)
		}
	}

	return h
}

// ─── Options parsing ─────────────────────────────────────────────────────────

type parsedHTTPOpts struct {
	headers nethttp.Header // per-call headers (level 5); a nil slice at a key signals deletion
	query   url.Values     // per-call query parameters

	hasTimeout bool
	timeout    time.Duration

	hasRedirectOverride bool
	followRedirects     bool
	maxRedirects        int

	as        string // "none", "string", "bytes", "json"
	bodyLimit int64
}

func parseOpts(opts cty.Value) (parsedHTTPOpts, error) {
	out := parsedHTTPOpts{as: "none"}
	if opts.IsNull() {
		return out, nil
	}
	if !opts.Type().IsObjectType() && !opts.Type().IsMapType() {
		return out, fmt.Errorf("opts must be an object or map, got %s", opts.Type().FriendlyName())
	}

	attrs := opts.AsValueMap()

	if v, ok := attrs["headers"]; ok && !v.IsNull() {
		h, err := parsePerCallHeaders(v)
		if err != nil {
			return out, fmt.Errorf("opts.headers: %w", err)
		}
		out.headers = h
	}

	if v, ok := attrs["query"]; ok && !v.IsNull() {
		q, err := parsePerCallQuery(v)
		if err != nil {
			return out, fmt.Errorf("opts.query: %w", err)
		}
		out.query = q
	}

	if v, ok := attrs["timeout"]; ok && !v.IsNull() {
		d, err := cfg.ParseDurationFromValue(v)
		if err != nil {
			return out, fmt.Errorf("opts.timeout: %w", err)
		}
		out.hasTimeout = true
		out.timeout = d
	}

	if v, ok := attrs["follow_redirects"]; ok && !v.IsNull() {
		if v.Type() != cty.Bool {
			return out, fmt.Errorf("opts.follow_redirects must be a bool, got %s", v.Type().FriendlyName())
		}
		out.hasRedirectOverride = true
		out.followRedirects = v.True()
		out.maxRedirects = 10 // spec default; may be overridden below
	}

	if v, ok := attrs["max_redirects"]; ok && !v.IsNull() {
		if v.Type() != cty.Number {
			return out, fmt.Errorf("opts.max_redirects must be a number, got %s", v.Type().FriendlyName())
		}
		n, _ := v.AsBigFloat().Int64()
		if n < 0 {
			return out, fmt.Errorf("opts.max_redirects must be >= 0")
		}
		out.hasRedirectOverride = true
		if !hasAttr(attrs, "follow_redirects") {
			out.followRedirects = true // default when only max_redirects is set
		}
		out.maxRedirects = int(n)
	}

	if v, ok := attrs["as"]; ok && !v.IsNull() {
		if v.Type() != cty.String {
			return out, fmt.Errorf("opts.as must be a string, got %s", v.Type().FriendlyName())
		}
		as := v.AsString()
		switch as {
		case "none", "string", "bytes", "json":
			out.as = as
		default:
			return out, fmt.Errorf("opts.as must be one of \"none\", \"string\", \"bytes\", \"json\"; got %q", as)
		}
	}

	if v, ok := attrs["body_limit"]; ok && !v.IsNull() {
		if v.Type() != cty.Number {
			return out, fmt.Errorf("opts.body_limit must be a number, got %s", v.Type().FriendlyName())
		}
		n, _ := v.AsBigFloat().Int64()
		if n < 0 {
			return out, fmt.Errorf("opts.body_limit must be >= 0")
		}
		out.bodyLimit = n
	}

	return out, nil
}

func hasAttr(m map[string]cty.Value, name string) bool {
	v, ok := m[name]
	return ok && !v.IsNull()
}

// parsePerCallHeaders converts an opts.headers cty value into an http.Header.
// Null map values are preserved as nil slices so buildRequestHeaders can
// interpret them as "delete this header".
func parsePerCallHeaders(val cty.Value) (nethttp.Header, error) {
	if !val.Type().IsMapType() && !val.Type().IsObjectType() {
		return nil, fmt.Errorf("must be a map or object, got %s", val.Type().FriendlyName())
	}
	out := make(nethttp.Header)
	for name, elem := range val.AsValueMap() {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if elem.IsNull() {
			out[canonical] = nil // signal: delete
			continue
		}
		switch {
		case elem.Type() == cty.String:
			out[canonical] = []string{elem.AsString()}
		case elem.Type().IsListType() || elem.Type().IsTupleType():
			vals := []string{}
			for it := elem.ElementIterator(); it.Next(); {
				_, v := it.Element()
				if v.IsNull() || v.Type() != cty.String {
					return nil, fmt.Errorf("header %q list values must be strings", name)
				}
				vals = append(vals, v.AsString())
			}
			out[canonical] = vals
		default:
			return nil, fmt.Errorf("header %q value must be a string or list of strings, got %s", name, elem.Type().FriendlyName())
		}
	}
	return out, nil
}

// parsePerCallQuery converts an opts.query cty value into a url.Values.
func parsePerCallQuery(val cty.Value) (url.Values, error) {
	if !val.Type().IsMapType() && !val.Type().IsObjectType() {
		return nil, fmt.Errorf("must be a map or object, got %s", val.Type().FriendlyName())
	}
	out := url.Values{}
	for name, elem := range val.AsValueMap() {
		if elem.IsNull() {
			continue
		}
		switch {
		case elem.Type() == cty.String:
			out.Add(name, elem.AsString())
		case elem.Type() == cty.Number:
			out.Add(name, elem.AsBigFloat().Text('f', -1))
		case elem.Type() == cty.Bool:
			if elem.True() {
				out.Add(name, "true")
			} else {
				out.Add(name, "false")
			}
		case elem.Type().IsListType() || elem.Type().IsTupleType():
			for it := elem.ElementIterator(); it.Next(); {
				_, v := it.Element()
				if v.IsNull() {
					continue
				}
				switch v.Type() {
				case cty.String:
					out.Add(name, v.AsString())
				case cty.Number:
					out.Add(name, v.AsBigFloat().Text('f', -1))
				case cty.Bool:
					if v.True() {
						out.Add(name, "true")
					} else {
						out.Add(name, "false")
					}
				default:
					return nil, fmt.Errorf("query %q list values must be strings, numbers, or bools", name)
				}
			}
		default:
			return nil, fmt.Errorf("query %q value must be a string, number, bool, or list thereof, got %s", name, elem.Type().FriendlyName())
		}
	}
	return out, nil
}
