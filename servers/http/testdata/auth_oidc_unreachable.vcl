// An OIDC-protected route whose issuer is not listening. The config must still
// build — validating or starting a config cannot require the identity provider
// to be reachable — and the route must answer 503 rather than let anything
// through.
server "http" "main" {
  listen = "127.0.0.1:0"

  auth "oidc" {
    issuer = env.VINCULUM_TEST_OIDC_ISSUER
  }

  handle "/private" {
    action = ctx.auth.subject
  }
}
