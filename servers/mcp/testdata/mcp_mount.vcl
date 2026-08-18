server "mcp" "mounted" {
    server_name = "Mounted MCP Server"

    tool "echo" {
        description = "Echo the input text"

        param "text" {
            type     = "string"
            required = true
        }

        action = ctx.args.text
    }
}

server "http" "main" {
    listen = ":19100"

    handle "/mcp/" {
        handler = server.mounted
    }
}
