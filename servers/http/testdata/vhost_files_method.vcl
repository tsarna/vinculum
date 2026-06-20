server "http" "main" {
  listen = "127.0.0.1:0"

  files "GET /static" {
    directory = "./static"
  }
}
