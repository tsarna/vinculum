server "mcp" "test" {
    listen      = ":19001"
    server_name = "Test MCP Server"

    resource "status://current" {
        name        = "Status"
        description = "Current system status"
        mime_type   = "text/plain"
        action      = "OK"
    }

    resource "info://{section}" {
        name        = "Info"
        description = "Info by section"
        action      = "section: ${ctx.section}"
    }
}
