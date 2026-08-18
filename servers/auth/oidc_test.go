package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// signingKey is a private key plus the public JWK an issuer would publish for it.
type signingKey struct {
	priv   *rsa.PrivateKey
	alg    jwa.SignatureAlgorithm
	kid    string
	public jwk.Key
}

// newSigningKey builds an RSA key advertising alg/kid, as a real JWKS entry would.
func newSigningKey(t *testing.T, alg jwa.SignatureAlgorithm, kid string) *signingKey {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	pub, err := jwk.Import(priv.Public())
	if err != nil {
		t.Fatalf("importing public key: %v", err)
	}
	if err := pub.Set(jwk.KeyIDKey, kid); err != nil {
		t.Fatalf("setting kid: %v", err)
	}
	if err := pub.Set(jwk.AlgorithmKey, alg); err != nil {
		t.Fatalf("setting alg: %v", err)
	}
	return &signingKey{priv: priv, alg: alg, kid: kid, public: pub}
}

// sign issues a signed JWT carrying the given claims.
func (k *signingKey) sign(t *testing.T, build func(*jwt.Builder) *jwt.Builder) string {
	t.Helper()

	tok, err := build(jwt.NewBuilder()).Build()
	if err != nil {
		t.Fatalf("building token: %v", err)
	}
	priv, err := jwk.Import(k.priv)
	if err != nil {
		t.Fatalf("importing private key: %v", err)
	}
	if err := priv.Set(jwk.KeyIDKey, k.kid); err != nil {
		t.Fatalf("setting kid: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(k.alg, priv))
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return string(signed)
}

// newIssuer starts a test OIDC issuer serving discovery and JWKS documents.
//
// The JWKS is assembled as raw JSON rather than through jwk.Set, because a real
// issuer can serve documents that jwk.Set will not hold — notably the same key
// listed twice, which AddKey rejects.
func newIssuer(t *testing.T, keys ...*signingKey) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeDiscovery(w, srv.URL)
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(w, keys...)
	})

	return srv
}

// writeDiscovery serves the discovery document an issuer at base would publish.
func writeDiscovery(w http.ResponseWriter, base string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(OIDCMetadata{
		Issuer:                base,
		AuthorizationEndpoint: base + "/authorize",
		TokenEndpoint:         base + "/token",
		JWKSUri:               base + "/jwks.json",
	})
}

// writeJWKS serves the given keys as a JWKS document.
func writeJWKS(w http.ResponseWriter, keys ...*signingKey) {
	entries := make([]json.RawMessage, len(keys))
	for i, k := range keys {
		encoded, err := json.Marshal(k.public)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries[i] = encoded
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": entries})
}

// expr parses src into a real HCL expression, so IsExpressionProvided sees a
// non-zero source range the way a parsed .vcl file would.
func expr(t *testing.T, src string) hcl.Expression {
	t.Helper()

	e, diags := hclsyntax.ParseExpression([]byte(src), "test.vcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing expression %q: %v", src, diags)
	}
	return e
}

// newTestOIDC builds an OIDC authenticator and stops it when the test ends, so
// no JWKS refresher outlives the test that created it.
func newTestOIDC(t *testing.T, ac *cfg.AuthConfig) Authenticator {
	t.Helper()

	a, err := newOIDCAuthenticator(ac, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("newOIDCAuthenticator: %v", err)
	}
	if s, ok := a.(interface{ Stop() error }); ok {
		t.Cleanup(func() { _ = s.Stop() })
	}
	return a
}

// authenticate runs a request carrying token through the authenticator.
func authenticate(t *testing.T, a Authenticator, token string) (cty.Value, *AuthFailure, error) {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return a.Authenticate(r, nil)
}

func TestOIDCAuthenticatorValidToken(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:     "oidc",
		Issuer:   issuer.URL,
		Audience: expr(t, `["api.example.com"]`),
	})

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").
			Issuer(issuer.URL).
			Audience([]string{"api.example.com"}).
			IssuedAt(time.Now()).
			Expiration(time.Now().Add(time.Hour)).
			Claim("preferred_username", "alice").
			Claim("groups", []string{"admins", "devs"}).
			Claim("email_verified", true)
	})

	val, failure, err := authenticate(t, a, token)
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if failure != nil {
		t.Fatalf("Authenticate rejected a valid token: %+v", failure)
	}

	if got := val.GetAttr("subject").AsString(); got != "user-42" {
		t.Errorf("subject = %q, want %q", got, "user-42")
	}
	if got := val.GetAttr("username").AsString(); got != "alice" {
		t.Errorf("username = %q, want %q", got, "alice")
	}

	claims := val.GetAttr("claims")
	if got := claims.GetAttr("sub").AsString(); got != "user-42" {
		t.Errorf("claims.sub = %q, want %q", got, "user-42")
	}
	if got := claims.GetAttr("iss").AsString(); got != issuer.URL {
		t.Errorf("claims.iss = %q, want %q", got, issuer.URL)
	}
	if !claims.Type().HasAttribute("exp") {
		t.Error("claims.exp missing")
	}
	if !claims.Type().HasAttribute("iat") {
		t.Error("claims.iat missing")
	}

	// aud is promoted to a list, and private claims survive the round trip.
	aud := claims.GetAttr("aud")
	if got := aud.LengthInt(); got != 1 {
		t.Fatalf("claims.aud length = %d, want 1", got)
	}
	if got := aud.Index(cty.NumberIntVal(0)).AsString(); got != "api.example.com" {
		t.Errorf("claims.aud[0] = %q, want %q", got, "api.example.com")
	}
	if got := claims.GetAttr("groups").LengthInt(); got != 2 {
		t.Errorf("claims.groups length = %d, want 2", got)
	}
	if !claims.GetAttr("email_verified").True() {
		t.Error("claims.email_verified = false, want true")
	}
}

// TestOIDCAuthenticatorRejectsDisallowedAlgorithm is the regression test for the
// algorithms list being parsed but never enforced: the issuer's key is perfectly
// valid and the signature verifies, but RS512 is outside the configured set.
func TestOIDCAuthenticatorRejectsDisallowedAlgorithm(t *testing.T) {
	allowed := newSigningKey(t, jwa.RS256(), "key-rs256")
	disallowed := newSigningKey(t, jwa.RS512(), "key-rs512")
	issuer := newIssuer(t, allowed, disallowed)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:       "oidc",
		Issuer:     issuer.URL,
		Algorithms: expr(t, `["RS256"]`),
	})

	claims := func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").
			Issuer(issuer.URL).
			IssuedAt(time.Now()).
			Expiration(time.Now().Add(time.Hour))
	}

	// The permitted algorithm still works...
	if _, failure, err := authenticate(t, a, allowed.sign(t, claims)); err != nil || failure != nil {
		t.Fatalf("RS256 token rejected: failure=%+v err=%v", failure, err)
	}

	// ...and the excluded one does not.
	_, failure, err := authenticate(t, a, disallowed.sign(t, claims))
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if failure == nil {
		t.Fatal("RS512 token accepted, but algorithms = [\"RS256\"]")
	}
	if failure.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", failure.Status, http.StatusUnauthorized)
	}
}

// TestOIDCAuthenticatorDuplicateJWKSEntry checks that an issuer serving the same
// key twice still authenticates. Filtering the key set rebuilds it entry by
// entry, so a malformed or repetitive JWKS is worth pinning: it is the issuer's
// to get wrong, and an operator cannot fix it from their own config.
func TestOIDCAuthenticatorDuplicateJWKSEntry(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key, key) // published twice

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:   "oidc",
		Issuer: issuer.URL,
	})

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").Expiration(time.Now().Add(time.Hour))
	})
	if _, failure, err := authenticate(t, a, token); err != nil || failure != nil {
		t.Fatalf("valid token rejected because the JWKS lists a key twice: failure=%+v err=%v", failure, err)
	}
}

func TestOIDCAuthenticatorRejectsBadTokens(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	other := newSigningKey(t, jwa.RS256(), "key-1") // same kid, wrong key
	issuer := newIssuer(t, key)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:     "oidc",
		Issuer:   issuer.URL,
		Audience: expr(t, `["api.example.com"]`),
	})

	base := func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").
			Issuer(issuer.URL).
			Audience([]string{"api.example.com"}).
			IssuedAt(time.Now()).
			Expiration(time.Now().Add(time.Hour))
	}

	tests := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{
			name:       "no authorization header",
			token:      "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed token",
			token:      "not-a-jwt",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "signed by an unknown key",
			token:      other.sign(t, base),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired",
			token: key.sign(t, func(b *jwt.Builder) *jwt.Builder {
				return base(b).Expiration(time.Now().Add(-time.Hour))
			}),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong audience",
			token: key.sign(t, func(b *jwt.Builder) *jwt.Builder {
				return base(b).Audience([]string{"other.example.com"})
			}),
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, failure, err := authenticate(t, a, tc.token)
			if err != nil {
				t.Fatalf("Authenticate returned error: %v", err)
			}
			if failure == nil {
				t.Fatal("token accepted, want rejection")
			}
			if failure.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", failure.Status, tc.wantStatus)
			}
		})
	}
}

// TestOIDCAuthenticatorClockSkew checks that a token that has only just expired
// is still accepted within the configured tolerance.
func TestOIDCAuthenticatorClockSkew(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:      "oidc",
		Issuer:    issuer.URL,
		ClockSkew: expr(t, `"5m"`),
	})

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").
			IssuedAt(time.Now().Add(-time.Hour)).
			Expiration(time.Now().Add(-time.Minute))
	})

	_, failure, err := authenticate(t, a, token)
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if failure != nil {
		t.Fatalf("token expired 1m ago rejected despite clock_skew = 5m: %+v", failure)
	}
}

// TestOIDCAuthenticatorJWKSUrlSkipsDiscovery checks the explicit-JWKS path,
// where no discovery document is ever fetched and there is none to expose.
func TestOIDCAuthenticatorJWKSUrlSkipsDiscovery(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key)

	a := newTestOIDC(t, &cfg.AuthConfig{
		Mode:    "oidc",
		JWKSUrl: issuer.URL + "/jwks.json",
	})

	token := key.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Subject("user-42").Expiration(time.Now().Add(time.Hour))
	})
	if _, failure, err := authenticate(t, a, token); err != nil || failure != nil {
		t.Fatalf("valid token rejected: failure=%+v err=%v", failure, err)
	}

	oidcAuth := a.(*oidcAuthenticator)
	oidcAuth.mu.Lock()
	meta := oidcAuth.resolved.meta
	oidcAuth.mu.Unlock()
	if meta != nil {
		t.Errorf("a discovery document was fetched (%+v) despite an explicit jwks_url", meta)
	}
}

func TestOIDCAuthenticatorConfigErrors(t *testing.T) {
	key := newSigningKey(t, jwa.RS256(), "key-1")
	issuer := newIssuer(t, key)

	tests := []struct {
		name string
		ac   *cfg.AuthConfig
	}{
		{
			name: "unknown algorithm",
			ac: &cfg.AuthConfig{
				Mode:       "oidc",
				Issuer:     issuer.URL,
				Algorithms: expr(t, `["RS256", "HS999"]`),
			},
		},
		{
			name: "empty algorithms",
			ac: &cfg.AuthConfig{
				Mode:       "oidc",
				Issuer:     issuer.URL,
				Algorithms: expr(t, `[]`),
			},
		},
		{
			name: "unsigned tokens are not an algorithm",
			ac: &cfg.AuthConfig{
				Mode:       "oidc",
				Issuer:     issuer.URL,
				Algorithms: expr(t, `["none"]`),
			},
		},
		{
			name: "algorithms is not a list of strings",
			ac: &cfg.AuthConfig{
				Mode:       "oidc",
				Issuer:     issuer.URL,
				Algorithms: expr(t, `"RS256"`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newOIDCAuthenticator(tc.ac, nil, zap.NewNop()); err == nil {
				t.Fatal("newOIDCAuthenticator succeeded, want error")
			}
		})
	}
}
