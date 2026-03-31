server "http" "main" {
  listen = "127.0.0.1:0"

  auth "basic" {
    credentials = { alice = "secret" }
  }

  handle "/public" {
    auth "none" {}
    action = "public"
  }

  handle "/private" {
    action = "private"
  }
}
