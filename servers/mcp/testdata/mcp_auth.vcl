// An MCP server owns no listener, so authentication belongs to the route of the
// `server "http"` block that mounts it. The identity the route establishes
// reaches the tool as ctx.auth.
auth "custom" "header" {
  # Accept any request with a username; use it as the subject.
  action = ctx.request.user != "" ? { subject = ctx.request.user } : null
}

server "mcp" "auth_test" {
    server_name = "Auth Test Server"

    tool "whoami" {
        description = "Return the authenticated subject"
        action      = ctx.auth.subject
    }
}

server "http" "main" {
    listen = "127.0.0.1:0"

    handle "/mcp" {
        handler = server.auth_test
        auth    = auth.header
    }
}
