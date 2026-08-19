auth "custom" "reject" {
  action = null
}

auth "custom" "accept" {
  action = { subject = "user" }
}

auth "custom" "login" {
  action = http::redirect("https://example.com/login")
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/fail" {
    auth   = auth.reject
    action = "ok"
  }

  handle "/succeed" {
    auth   = auth.accept
    action = ctx.auth.subject
  }

  handle "/redirect" {
    auth   = auth.login
    action = "logged in"
  }
}
