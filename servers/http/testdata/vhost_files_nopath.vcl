server "http" "main" {
  listen = "127.0.0.1:0"

  files "example.com" {
    directory = "./static"
  }
}
