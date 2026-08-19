auth "basic" "web" {
  credentials = { alice = "secret" }
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.web

  handle "/public" {
    auth   = auth.anonymous
    action = "public"
  }

  handle "/private" {
    action = "private"
  }
}
