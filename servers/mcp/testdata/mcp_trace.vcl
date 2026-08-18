server "mcp" "trace_test" {
    server_name = "Trace Test MCP Server"

    resource "status://current" {
        name   = "Status"
        action = "OK"
    }
}
