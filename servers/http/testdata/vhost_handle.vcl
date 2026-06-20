server "http" "main" {
  listen = "127.0.0.1:0"

  handle "api.example.com/where" {
    action = "api"
  }

  handle "cdn.example.com/where" {
    action = "cdn"
  }

  handle "/where" {
    action = "default"
  }
}
