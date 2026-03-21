server "mcp" "tools_test" {
    listen      = ":19002"
    server_name = "Tools Test Server"

    tool "echo" {
        description = "Echo the input text"

        param "text" {
            type     = "string"
            required = true
        }

        action = ctx.args.text
    }

    tool "greet" {
        description = "Greet someone"

        param "name" {
            type        = "string"
            description = "Name to greet"
            required    = true
        }

        action = "Hello, ${ctx.args.name}!"
    }
}
