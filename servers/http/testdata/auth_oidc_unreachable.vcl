// An OIDC-protected route whose issuer is not listening. The config must still
// build — validating or starting a config cannot require the identity provider
// to be reachable — and the route must answer 503 rather than let anything
// through.
auth "oidc" "corp" {
  issuer = env.VINCULUM_TEST_OIDC_ISSUER
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.corp

  handle "/private" {
    action = ctx.auth.subject
  }
}
