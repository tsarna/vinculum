server "http" "main" {
  listen = "127.0.0.1:0"

  auth "basic" {
    credentials = { alice = "secret" }
  }

  files "/public" {
    auth "none" {}
    directory = "/tmp"
  }
}
