server "http" "main" {
  listen = "127.0.0.1:0"

  handle "api.example.com/dup" {
    action = "first"
  }

  handle "api.example.com/dup" {
    action = "second"
  }
}
