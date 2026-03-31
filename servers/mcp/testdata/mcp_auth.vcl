server "mcp" "auth_test" {
    listen      = ":19009"
    server_name = "Auth Test Server"

    auth "custom" {
        # Accept any request with an "X-User" header; use its value as the subject.
        action = ctx.request.user != "" ? { subject = ctx.request.user } : null
    }

    tool "whoami" {
        description = "Return the authenticated subject"
        action      = ctx.auth.subject
    }
}
