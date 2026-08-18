package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// shortenAuthFetch swaps the package-wide fetch timeout (and the client built
// from it) for the duration of one test, so a test for the timeout does not
// have to wait the production ten seconds.
func shortenAuthFetch(t *testing.T, d time.Duration) {
	t.Helper()

	oldTimeout, oldClient := authFetchTimeout, authHTTPClient
	authFetchTimeout = d
	authHTTPClient = &http.Client{Timeout: d}
	t.Cleanup(func() {
		authFetchTimeout, authHTTPClient = oldTimeout, oldClient
	})
}

// deadIssuerURL returns a URL that nothing is listening on: an httptest server
// started and immediately closed, so the port is known to be free.
func deadIssuerURL(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// TestOIDCUnreachableIssuerIsNotFatal is the regression test for the original
// report: an identity provider that is down at startup used to fail the whole
// config, so `vinculum serve` refused to run and `vinculum check` could not
// validate a file without network access to the provider.
//
// Construction must now succeed, and Start must neither block nor fail.
func TestOIDCUnreachableIssuerIsNotFatal(t *testing.T) {
	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: deadIssuerURL(t),
	})

	starter, ok := a.(interface{ Start() error })
	if !ok {
		t.Fatalf("%T does not implement Start", a)
	}

	done := make(chan error, 1)
	go func() { done <- starter.Start() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v; an unreachable issuer must not fail startup", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return; an unreachable issuer must not block startup")
	}
}

// TestOIDCUnreachableIssuerRejects checks the other half of the bargain: having
// not failed at startup, the authenticator must not let anything through.
func TestOIDCUnreachableIssuerRejects(t *testing.T) {
	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: deadIssuerURL(t),
	})

	// A syntactically fine token that no key could have signed.
	val, failure, err := authenticate(t, a, "eyJhbGciOiJSUzI1NiJ9.e30.sig")
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if failure == nil {
		t.Fatalf("request accepted while the issuer was unreachable (auth value %#v) — this is a fail-open", val)
	}
	if failure.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", failure.Status, http.StatusServiceUnavailable)
	}
	if failure.Response == nil {
		t.Fatal("no Response on the failure, so no Retry-After can be sent")
	}
	if got := failure.Response.Headers.Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing from the 503")
	}
}

// TestOIDCMissingTokenStillGets401 pins the ordering: a request with no
// credential at all is wrong regardless of the issuer's state, and answering it
// costs no network round trip.
func TestOIDCMissingTokenStillGets401(t *testing.T) {
	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: deadIssuerURL(t),
	})

	_, failure, err := authenticate(t, a, "")
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if failure == nil {
		t.Fatal("request with no Authorization header accepted")
	}
	if failure.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", failure.Status, http.StatusUnauthorized)
	}
}

// TestOIDCHungIssuerTimesOut covers the failure mode that is worse than a
// refused connection: an issuer that accepts the connection and then never
// answers. Without a timeout on the fetch this hangs forever with no
// diagnostic at all.
func TestOIDCHungIssuerTimesOut(t *testing.T) {
	shortenAuthFetch(t, 100*time.Millisecond)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hung.Close)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: hung.URL,
	})

	type result struct {
		failure *AuthFailure
		err     error
	}
	done := make(chan result, 1)
	go func() {
		_, failure, err := authenticate(t, a, "eyJhbGciOiJSUzI1NiJ9.e30.sig")
		done <- result{failure, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Authenticate returned error: %v", got.err)
		}
		if got.failure == nil {
			t.Fatal("request accepted while the issuer was hung")
		}
		if got.failure.Status != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", got.failure.Status, http.StatusServiceUnavailable)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authenticate never returned: the fetch has no timeout")
	}
}

// TestOIDCCallerCancellationDoesNotAbortTheFetch checks that a caller giving up
// only ends that caller's wait. The attempt belongs to the authenticator, not
// to whichever request happened to trigger it, so one client disconnecting must
// not abort a fetch every other waiter is depending on.
func TestOIDCCallerCancellationDoesNotAbortTheFetch(t *testing.T) {
	release := make(chan struct{})
	var discoveries atomic.Int32

	key := newSigningKey(t, jwa.RS256(), "key-1")
	var issuer *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		discoveries.Add(1)
		<-release
		writeDiscovery(w, issuer.URL)
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(w, key)
	})
	issuer = httptest.NewServer(mux)
	t.Cleanup(issuer.Close)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: issuer.URL,
	})

	// First caller starts the fetch, then walks away.
	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan struct{})
	go func() {
		defer close(abandoned)
		r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer x")
		_, _, _ = a.Authenticate(r, nil)
	}()

	// Wait for the fetch to be underway before cancelling.
	waitFor(t, func() bool { return discoveries.Load() == 1 })
	cancel()
	<-abandoned

	// The fetch it started is still in flight; let it finish.
	close(release)

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").Expiration(time.Now().Add(time.Hour))
	})
	if _, failure, err := authenticate(t, a, token); err != nil || failure != nil {
		t.Fatalf("valid token rejected after an unrelated caller cancelled: failure=%+v err=%v", failure, err)
	}
	if got := discoveries.Load(); got != 1 {
		t.Errorf("discovery fetched %d times, want 1 — the abandoned attempt was restarted", got)
	}
}

// TestOIDCNegativeCaching checks that a down issuer costs one fetch per backoff
// interval rather than one per request. Without it, a provider outage turns
// every inbound request into an outbound connection attempt.
func TestOIDCNegativeCaching(t *testing.T) {
	var discoveries atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveries.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(issuer.Close)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: issuer.URL,
	})

	for range 5 {
		if _, failure, err := authenticate(t, a, "eyJhbGciOiJSUzI1NiJ9.e30.sig"); err != nil || failure == nil {
			t.Fatalf("request not rejected: failure=%+v err=%v", failure, err)
		}
	}

	if got := discoveries.Load(); got != 1 {
		t.Errorf("issuer contacted %d times for 5 requests, want 1 — failures are not being cached", got)
	}
}

// TestOIDCRecoversWhenIssuerComesUp is principle "self-heal" in one test: a
// provider that was down when the first request arrived must be picked up
// without restarting the process.
//
// The backoff deadline is cleared directly rather than slept through; the
// schedule itself is covered by TestNextBackoff.
func TestOIDCRecoversWhenIssuerComesUp(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")

	var healthy atomic.Bool
	var issuer *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		writeDiscovery(w, issuer.URL)
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(w, key)
	})
	issuer = httptest.NewServer(mux)
	t.Cleanup(issuer.Close)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: issuer.URL,
	})

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").Expiration(time.Now().Add(time.Hour))
	})

	if _, failure, _ := authenticate(t, a, token); failure == nil {
		t.Fatal("token accepted while the issuer was down")
	} else if failure.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", failure.Status, http.StatusServiceUnavailable)
	}

	healthy.Store(true)
	oidcAuth := a.(*oidcAuthenticator)
	oidcAuth.mu.Lock()
	oidcAuth.nextTry = time.Time{} // skip the backoff wait
	oidcAuth.mu.Unlock()

	if _, failure, err := authenticate(t, a, token); err != nil || failure != nil {
		t.Fatalf("token still rejected after the issuer came up: failure=%+v err=%v", failure, err)
	}
}

func TestNextBackoff(t *testing.T) {
	got := time.Duration(0)
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		got = nextBackoff(got)
		if got != want*time.Second {
			t.Fatalf("nextBackoff = %v, want %v", got, want*time.Second)
		}
	}
}

// TestOIDCStopEndsRefreshing checks that shutdown does not leave a JWKS
// refresher polling the issuer for the life of the process.
func TestOIDCStopEndsRefreshing(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key)

	a, err := newOIDCAuthenticator(&cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: issuer.URL,
	}, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("newOIDCAuthenticator: %v", err)
	}

	oidcAuth := a.(*oidcAuthenticator)
	if _, err := oidcAuth.resolve(context.Background()); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if err := oidcAuth.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if oidcAuth.ctx.Err() == nil {
		t.Error("Stop did not cancel the authenticator context")
	}
}

// waitFor polls cond until it holds, failing the test if it never does.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
