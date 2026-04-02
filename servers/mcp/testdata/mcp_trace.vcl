server "mcp" "trace_test" {
    listen      = ":0"
    server_name = "Trace Test MCP Server"

    resource "status://current" {
        name   = "Status"
        action = "OK"
    }
}
