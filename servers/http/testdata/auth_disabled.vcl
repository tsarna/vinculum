// disabled = true with no credentials: the block must parse without the
// mechanism-specific "basic needs credentials or action" error, the name must
// still resolve, and requests must not be challenged. Mirrors the
// traffic-light example's env toggle.
auth "basic" "web" {
  disabled = true
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.web

  handle "/whoami" {
    action = "ok"
  }
}
