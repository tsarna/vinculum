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

	"github.com/hashicorp/hcl/v2"
	clientshttp "github.com/tsarna/vinculum/clients/http"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func init() {
	cfg.RegisterFunctionPlugin("http", func(c *cfg.Config) map[string]function.Function {
		return GetHTTPClientFunctions(c)
	})
}

// GetHTTPClientFunctions returns the eight http_* verb functions, capturing
// the supplied config so verb functions can evaluate lazy expressions like
// retry.on_response against the same eval context the rest of vinculum uses.
// config may be nil in tests that do not exercise hooks.
func GetHTTPClientFunctions(config *cfg.Config) map[string]function.Function {
	return map[string]function.Function{
		"http_get":     makeVerbFunc(config, "GET", false),
		"http_head":    makeVerbFunc(config, "HEAD", false),
		"http_options": makeVerbFunc(config, "OPTIONS", false),
		"http_delete":  makeVerbFunc(config, "DELETE", false),
		"http_post":    makeVerbFunc(config, "POST", true),
		"http_put":     makeVerbFunc(config, "PUT", true),
		"http_patch":   makeVerbFunc(config, "PATCH", true),
		"http_request": makeGenericVerbFunc(config),
	}
}

// ─── Verb function constructors ──────────────────────────────────────────────

// makeVerbFunc builds a verb function for a fixed HTTP method. Body-bearing
// verbs accept (ctx, client, url, body?, opts?); body-less verbs accept
// (ctx, client, url, opts?). The trailing optional slots are resolved by
// argument count at call time.
func makeVerbFunc(config *cfg.Config, method string, bodyAllowed bool) function.Function {
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
			return doHTTPRequest(config, ctx, client, method, rawURL, body, opts)
		},
	})
}

// makeGenericVerbFunc builds the generic http_request(ctx, client, method,
// url, body?, opts?) function.
func makeGenericVerbFunc(config *cfg.Config) function.Function {
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
			return doHTTPRequest(config, ctx, client, method, rawURL, body, opts)
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

// doHTTPRequest is the top-level dispatcher for a verb function call. It
// resolves the URL, coerces the body, parses opts, picks the effective
// retry policy, and then drives the retry loop.
func doHTTPRequest(
	config *cfg.Config,
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
	parsedOpts, err := parseOpts(config, opts)
	if err != nil {
		return cty.NilVal, fmt.Errorf("%s: opts: %w", verbName, err)
	}

	// ── Query parameters from opts.query ──
	if len(parsedOpts.query) > 0 {
		q := reqURL.Query()
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

	// ── Per-call timeout override (applied via context derivation) ──
	callCtx := ctx
	if parsedOpts.hasTimeout {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, parsedOpts.timeout)
		defer cancel()
	}

	// ── Resolve effective retry policy ──
	// Priority: opts.retry { ... } > opts.retry = false > client default.
	effectiveRetry := client.DefaultRetryPolicy()
	if parsedOpts.retryDisabled {
		effectiveRetry = noRetryFromBase(effectiveRetry)
	} else if parsedOpts.retryOverride != nil {
		effectiveRetry = *parsedOpts.retryOverride
	}

	// ── Drive the retry loop ──
	return runRetryLoop(
		config,
		client,
		callCtx,
		method,
		verbName,
		reqURL,
		hdrs,
		bodyBytes,
		parsedOpts,
		effectiveRetry,
	)
}

// noRetryFromBase returns a copy of base with MaxAttempts forced to 1. It
// preserves the rest of the policy (e.g. RetryOn, jitter settings) for
// debug visibility, even though they have no effect when MaxAttempts == 1.
func noRetryFromBase(base clientshttp.RetryPolicy) clientshttp.RetryPolicy {
	base.MaxAttempts = 1
	return base
}

// runRetryLoop executes the request up to MaxAttempts times, computing the
// inter-attempt delay from the retry policy and consulting the on_response
// hook (if any) after each attempt that produced a response. Transport
// errors are retryable when the method is retryable under the policy.
func runRetryLoop(
	config *cfg.Config,
	client clientshttp.HTTPCallable,
	callCtx context.Context,
	method string,
	verbName string,
	reqURL *url.URL,
	hdrs nethttp.Header,
	bodyBytes []byte,
	parsedOpts parsedHTTPOpts,
	pol clientshttp.RetryPolicy,
) (cty.Value, error) {
	maxAttempts := pol.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	methodRetryable := pol.MethodRetryable(method)

	var (
		lastWrapper *types.HTTPClientResponseWrapper
		lastTxErr   error
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait the configured delay before all attempts after the first.
		if attempt > 1 {
			delay := pol.Delay(attempt)
			// If the previous response carried a Retry-After header on a
			// status the spec covers, honor it instead of the computed
			// delay.
			if pol.RespectRetryAfter && lastWrapper != nil &&
				clientshttp.RetryAfterApplies(lastWrapper.R.StatusCode) {
				if ra := clientshttp.ParseRetryAfter(lastWrapper.R.Header.Get("Retry-After")); ra > 0 {
					delay = ra
				}
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-callCtx.Done():
					return cty.NilVal, fmt.Errorf("%s: %w", verbName, callCtx.Err())
				}
			}
		}

		wrapper, txErr := sendOnceWithAuth(config, callCtx, client, method, reqURL, hdrs, bodyBytes, parsedOpts)
		lastWrapper = wrapper
		lastTxErr = txErr

		// ── Decide whether to retry ──
		if attempt == maxAttempts {
			break
		}
		if !methodRetryable {
			break
		}

		retry := false
		if txErr != nil {
			// Network/transport error: retry if the method allows it.
			retry = true
		} else if pol.ShouldRetryStatus(wrapper.R.StatusCode) {
			retry = true
		}

		// on_response hook can override the default decision when the
		// attempt produced a response.
		if pol.OnResponse != nil && wrapper != nil && txErr == nil {
			decision, hookErr := evalOnResponse(config, client, pol.OnResponse, wrapper, attempt)
			if hookErr != nil {
				return cty.NilVal, fmt.Errorf("%s: retry.on_response: %w", verbName, hookErr)
			}
			switch d := decision.(type) {
			case onResponseRetry:
				retry = true
				if d.delay > 0 {
					// Hook-supplied delay overrides backoff for this hop.
					select {
					case <-time.After(d.delay):
					case <-callCtx.Done():
						return cty.NilVal, fmt.Errorf("%s: %w", verbName, callCtx.Err())
					}
					// Already slept; advance attempt directly without
					// computing backoff again.
					nextWrapper, nextErr := sendOnceWithAuth(config, callCtx, client, method, reqURL, hdrs, bodyBytes, parsedOpts)
					lastWrapper = nextWrapper
					lastTxErr = nextErr
					attempt++
					continue
				}
			case onResponseStop:
				retry = false
			}
		}

		if !retry {
			break
		}
	}

	if lastTxErr != nil {
		return cty.NilVal, fmt.Errorf("%s: %w", verbName, lastTxErr)
	}
	return finalizeResponse(lastWrapper, parsedOpts, verbName)
}

// sendOnce executes a single attempt: builds an *http.Request from the
// captured pieces, sends it through the client (with per-call redirect
// override if requested), buffers the response body, and returns a wrapper.
// A non-nil error means the request never produced a usable response —
// the caller decides whether to retry or surface the error.
func sendOnce(
	ctx context.Context,
	client clientshttp.HTTPCallable,
	method string,
	reqURL *url.URL,
	hdrs nethttp.Header,
	bodyBytes []byte,
	parsedOpts parsedHTTPOpts,
) (*types.HTTPClientResponseWrapper, error) {
	var bodyReader io.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := nethttp.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header = hdrs.Clone()
	if len(bodyBytes) > 0 {
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	origURL := *reqURL

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
		return nil, err
	}

	// Always buffer the response body up-front and close the live stream.
	data, readErr := readResponseBody(resp.Body, parsedOpts.bodyLimit)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read body: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close body: %w", closeErr)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))

	return &types.HTTPClientResponseWrapper{
		R:            resp,
		Redirected:   resp.Request != nil && resp.Request.URL != nil && !sameURL(&origURL, resp.Request.URL),
		BufferedBody: data,
	}, nil
}

// sendOnceWithAuth wraps sendOnce with the auth-injection layer described
// in HTTP-CLIENT-SPEC.md "Authentication". The flow per attempt:
//
//  1. Compute the effective Authorization header for this attempt:
//     - If opts.headers["Authorization"] is already set (level 5 of the
//       header precedence chain), use it — and skip the hook entirely.
//     - Else if opts.auth was set on this call, use that literal value
//       (or no Authorization header if opts.auth was null) — skip the
//       hook entirely.
//     - Else if the client has an AuthHandler and the reentrancy marker
//       is not set for this client, call AuthHandler.Get to fetch a
//       cached or freshly-evaluated value.
//     - Else send no Authorization header.
//
//  2. Send the request via sendOnce.
//
//  3. If the response was 401 AND the auth hook is in scope (i.e. the
//     AuthHandler is non-nil and we are not suppressing it for this call):
//     invalidate the cache, call Get with reason="unauthorized", and
//     re-send the request *once* with the new value. A 401 on that
//     re-attempt is returned to the caller as-is.
//
//  4. Record the final response status against the AuthHandler so it
//     can update its consecutive-failure counter.
func sendOnceWithAuth(
	config *cfg.Config,
	ctx context.Context,
	client clientshttp.HTTPCallable,
	method string,
	reqURL *url.URL,
	hdrs nethttp.Header,
	bodyBytes []byte,
	parsedOpts parsedHTTPOpts,
) (*types.HTTPClientResponseWrapper, error) {
	handler := client.AuthHandler()
	clientName := client.GetName()

	// hdrsHasAuth = the per-call header set already contains
	// Authorization (e.g. from opts.headers). Treat that as a hard
	// override that bypasses both the hook and opts.auth — the
	// strongest-wins rule from the header precedence chain.
	hdrsHasAuth := hdrs.Get("Authorization") != ""

	// Decide the source of the Authorization header for the FIRST send.
	authValue, useHook, err := resolveAuthForSend(
		ctx, client, handler, clientName, parsedOpts, hdrsHasAuth,
		"initial", 0,
	)
	if err != nil {
		return nil, err
	}

	// Apply the resolved Authorization unless an explicit per-call header
	// already supplied one.
	sendHdrs := hdrs
	if !hdrsHasAuth {
		sendHdrs = hdrs.Clone()
		if authValue != "" {
			sendHdrs.Set("Authorization", authValue)
		} else {
			sendHdrs.Del("Authorization")
		}
	}

	wrapper, txErr := sendOnce(ctx, client, method, reqURL, sendHdrs, bodyBytes, parsedOpts)
	if txErr != nil {
		// Transport error: the hook never got a wire-level result, so
		// nothing to record. Surface as-is.
		return nil, txErr
	}

	// 401 re-auth and retry, but only if the hook is actually in scope
	// for this call. opts.auth and opts.headers.Authorization both
	// disable hook-driven re-auth, and reentrancy suppression also
	// disables it (the outer caller already holds the cache mutex).
	if wrapper.R.StatusCode == 401 && useHook && handler != nil && !clientshttp.IsAuthSuppressed(ctx, clientName) {
		// Tell the cache that the value it just handed out failed; this
		// also invalidates the cached value.
		handler.RecordResult(401)

		// Re-fetch with reason="unauthorized". We pass previousStatus=401
		// so the hook expression's ctx.auth_attempt reflects the trigger.
		newAuth, _, reauthErr := resolveAuthForSend(
			ctx, client, handler, clientName, parsedOpts, false,
			"unauthorized", 401,
		)
		if reauthErr != nil {
			return nil, reauthErr
		}

		retryHdrs := hdrs.Clone()
		if newAuth != "" {
			retryHdrs.Set("Authorization", newAuth)
		} else {
			retryHdrs.Del("Authorization")
		}

		retryWrapper, retryTxErr := sendOnce(ctx, client, method, reqURL, retryHdrs, bodyBytes, parsedOpts)
		if retryTxErr != nil {
			return nil, retryTxErr
		}
		wrapper = retryWrapper
	}

	// Record the final outcome against the cache (only if the hook is
	// what produced the value — opts.auth and opts.headers paths bypass
	// the cache entirely). Also skip recording when the request was
	// sent under reentrancy suppression: in that case we are running
	// on a goroutine that already holds the outer Get → eval → http_get
	// stack with the cache mutex held above us, so reacquiring it here
	// would deadlock — and there is no cached value to update anyway.
	if useHook && handler != nil && !clientshttp.IsAuthSuppressed(ctx, clientName) {
		handler.RecordResult(wrapper.R.StatusCode)
	}

	_ = config // currently unused; reserved for future hooks that need it
	return wrapper, nil
}

// resolveAuthForSend computes the Authorization header value for one
// attempt according to the precedence rules in sendOnceWithAuth.
//
// Returns (value, useHook, err) where:
//   - value is the Authorization header to send ("" means none)
//   - useHook is true if the value came from the AuthHandler (and so the
//     401 re-auth path and RecordResult both apply); false if the value
//     came from opts.auth, opts.headers, or there is no auth at all
func resolveAuthForSend(
	ctx context.Context,
	client clientshttp.HTTPCallable,
	handler clientshttp.AuthHandler,
	clientName string,
	parsedOpts parsedHTTPOpts,
	hdrsHasAuth bool,
	reason string,
	previousStatus int,
) (string, bool, error) {
	// Strongest: explicit per-call header. The verb function should not
	// touch it; this branch is signalled by the caller already having set
	// it on hdrs.
	if hdrsHasAuth {
		return "", false, nil
	}

	// Per-call opts.auth: literal string or null bypass.
	if parsedOpts.hasAuthOverride {
		if parsedOpts.authValueSet {
			return parsedOpts.authValue, false, nil
		}
		// opts.auth = null → send no Authorization, bypass hook.
		return "", false, nil
	}

	// Hook path. Reentrancy suppression handled inside Get.
	if handler == nil {
		return "", false, nil
	}
	eval := func(attempt clientshttp.AuthAttempt) (clientshttp.AuthResult, error) {
		return evalAuthExpression(ctx, client, handler, clientName, attempt)
	}
	val, err := handler.Get(ctx, reason, previousStatus, eval)
	if err != nil {
		return "", true, err
	}
	return val, true, nil
}

// evalAuthExpression evaluates the auth HCL expression for the given
// client. The eval context contains the standard ctx attributes plus
// ctx.auth_attempt with the structured AuthAttempt fields. The Go
// context passed into the eval has the reentrancy marker for clientName
// added, so any http_*() call from inside the hook against the same
// client sees IsAuthSuppressed.
func evalAuthExpression(
	ctx context.Context,
	client clientshttp.HTTPCallable,
	handler clientshttp.AuthHandler,
	clientName string,
	attempt clientshttp.AuthAttempt,
) (clientshttp.AuthResult, error) {
	expr := handler.Expression()
	if expr == nil {
		return clientshttp.AuthResult{}, fmt.Errorf("auth handler has no expression")
	}

	parent := client.ParentEvalCtx()
	if parent == nil {
		return clientshttp.AuthResult{}, fmt.Errorf("auth: no eval context available for client %q", clientName)
	}

	// Mark this client as in-flight for the duration of the hook.
	hookCtx := clientshttp.WithAuthMarker(ctx, clientName)

	// Build the auth_attempt object exposed via ctx.auth_attempt.
	attemptObj := cty.ObjectVal(map[string]cty.Value{
		"reason":          cty.StringVal(attempt.Reason),
		"failures":        cty.NumberIntVal(int64(attempt.Failures)),
		"previous_status": cty.NumberIntVal(int64(attempt.PreviousStatus)),
	})

	evalCtx, err := hclutil.NewEvalContext(hookCtx).
		WithAttribute("auth_attempt", attemptObj).
		BuildEvalContext(parent)
	if err != nil {
		return clientshttp.AuthResult{}, fmt.Errorf("auth: build eval context: %w", err)
	}

	val, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		return clientshttp.AuthResult{}, fmt.Errorf("auth: evaluate expression: %w", diags)
	}

	return clientshttp.ParseAuthResult(val)
}

// finalizeResponse applies opts.as pre-decoding to the wrapper produced by
// the final attempt and builds the cty result object.
func finalizeResponse(
	wrapper *types.HTTPClientResponseWrapper,
	parsedOpts parsedHTTPOpts,
	verbName string,
) (cty.Value, error) {
	if wrapper == nil {
		return cty.NilVal, fmt.Errorf("%s: no response", verbName)
	}
	data := wrapper.BufferedBody

	switch parsedOpts.as {
	case "none":
		// No pre-decode; the buffered body is still available via get().
	case "string":
		wrapper.PreDecodedBody = cty.StringVal(string(data))
	case "bytes":
		wrapper.PreDecodedBody = types.BuildBytesObject(data, stripMIMEParams(wrapper.R.Header.Get("Content-Type")))
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

// ─── Retry option and on_response hook ──────────────────────────────────────

// parseRetryOpt decodes opts.retry. The value is one of:
//   - bool false: disable retries for this call
//   - object: an inline retry override (max_attempts, initial_delay, etc.)
//
// Returns (disabled, override, err) where exactly one of disabled or
// override is set when err is nil.
func parseRetryOpt(_ *cfg.Config, val cty.Value) (bool, *clientshttp.RetryPolicy, error) {
	if val.Type() == cty.Bool {
		if val.True() {
			return false, nil, fmt.Errorf("opts.retry = true is not supported (use a retry block or omit the option)")
		}
		return true, nil, nil
	}
	pol, err := clientshttp.ParseRetryPolicyFromValue(val)
	if err != nil {
		return false, nil, err
	}
	return false, &pol, nil
}

// onResponseDecision is the result of evaluating retry.on_response. It is
// either onResponseRetry (with an optional explicit delay) or
// onResponseStop. The hook may also return an error value, in which case
// runRetryLoop surfaces it directly.
type onResponseDecision interface {
	onResponseDecision()
}

type onResponseRetry struct {
	delay time.Duration
}

func (onResponseRetry) onResponseDecision() {}

type onResponseStop struct{}

func (onResponseStop) onResponseDecision() {}

// evalOnResponse evaluates the retry.on_response HCL expression against an
// eval context populated with ctx.response (the in-flight response wrapper)
// and ctx.attempt (1-indexed attempt number that just completed).
//
// Return value mapping per HTTP-CLIENT-SPEC.md:
//
//	number / duration → wait this long, then retry
//	true              → retry using normal backoff
//	false / null      → do not retry, return the response
func evalOnResponse(
	config *cfg.Config,
	client clientshttp.HTTPCallable,
	expr hcl.Expression,
	wrapper *types.HTTPClientResponseWrapper,
	attempt int,
) (onResponseDecision, error) {
	parent := client.ParentEvalCtx()
	if parent == nil && config != nil {
		parent = config.EvalCtx()
	}
	if parent == nil {
		return nil, fmt.Errorf("no eval context available for on_response hook")
	}

	respObj := types.BuildHTTPClientResponseObject(wrapper)
	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithAttribute("response", respObj).
		WithInt64Attribute("attempt", int64(attempt)).
		BuildEvalContext(parent)
	if err != nil {
		return nil, fmt.Errorf("build eval context: %w", err)
	}

	val, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	if val.IsNull() {
		return onResponseStop{}, nil
	}
	switch {
	case val.Type() == cty.Bool:
		if val.True() {
			return onResponseRetry{}, nil
		}
		return onResponseStop{}, nil
	case val.Type() == cty.Number:
		// Treat numbers as a delay in seconds (matches the rest of the
		// codebase's duration handling).
		secs, _ := val.AsBigFloat().Float64()
		if secs < 0 {
			return nil, fmt.Errorf("on_response delay must be >= 0, got %v", secs)
		}
		return onResponseRetry{delay: time.Duration(secs * float64(time.Second))}, nil
	default:
		// Try parsing as a duration value (string or duration capsule).
		d, err := cfg.ParseDurationFromValue(val)
		if err != nil {
			return nil, fmt.Errorf("on_response must return null, bool, number, or duration; got %s: %w", val.Type().FriendlyName(), err)
		}
		return onResponseRetry{delay: d}, nil
	}
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

	// Retry overrides. retryDisabled is set when opts.retry == false.
	// retryOverride is non-nil when opts.retry is a block. The two are
	// mutually exclusive.
	retryDisabled bool
	retryOverride *clientshttp.RetryPolicy

	// Per-call auth override. hasAuthOverride is true when opts.auth was
	// supplied (even with a null value, which means "send no Authorization
	// for this call and bypass the hook"). authValue is the literal header
	// to send when authValueSet is true.
	hasAuthOverride bool
	authValueSet    bool
	authValue       string

	as        string // "none", "string", "bytes", "json"
	bodyLimit int64
}

func parseOpts(config *cfg.Config, opts cty.Value) (parsedHTTPOpts, error) {
	out := parsedHTTPOpts{as: "none"}
	if opts.IsNull() {
		return out, nil
	}
	if !opts.Type().IsObjectType() && !opts.Type().IsMapType() {
		return out, fmt.Errorf("opts must be an object or map, got %s", opts.Type().FriendlyName())
	}

	attrs := opts.AsValueMap()

	if v, ok := attrs["retry"]; ok && !v.IsNull() {
		disabled, override, err := parseRetryOpt(config, v)
		if err != nil {
			return out, fmt.Errorf("opts.retry: %w", err)
		}
		out.retryDisabled = disabled
		out.retryOverride = override
	}

	if v, ok := attrs["auth"]; ok {
		// Even a null value here is meaningful: it means "do not send
		// Authorization for this call, and do not run the auth hook".
		out.hasAuthOverride = true
		if !v.IsNull() {
			if v.Type() != cty.String {
				return out, fmt.Errorf("opts.auth must be a string or null, got %s", v.Type().FriendlyName())
			}
			out.authValueSet = true
			out.authValue = v.AsString()
		}
	}

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
