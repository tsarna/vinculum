server "http" "main" {
  listen = "127.0.0.1:0"

  # disabled = true with no credentials: the block must parse without the
  # mode-specific "basic requires credentials or action" error, and requests
  # must not be challenged. Mirrors the traffic-light example's env toggle.
  auth "basic" {
    disabled = true
  }

  handle "/whoami" {
    action = "ok"
  }
}
