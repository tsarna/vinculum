server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/fail" {
    auth "custom" {
      action = null
    }
    action = "ok"
  }

  handle "/succeed" {
    auth "custom" {
      action = { subject = "user" }
    }
    action = ctx.auth.subject
  }

  handle "/redirect" {
    auth "custom" {
      action = http_redirect("https://example.com/login")
    }
    action = "logged in"
  }
}
