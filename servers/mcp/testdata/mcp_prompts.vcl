server "mcp" "prompts_test" {
    listen      = ":19003"
    server_name = "Prompts Test Server"

    prompt "summarize" {
        description = "Summarize a topic"

        param "topic" {
            type     = "string"
            required = true
        }

        action = mcp_usermessage("Please summarize: ${ctx.args.topic}")
    }

    prompt "translate" {
        description = "Translate text"

        param "text" {
            type     = "string"
            required = true
        }

        param "language" {
            type     = "string"
            required = true
        }

        action = mcp_usermessage("Translate to ${ctx.args.language}: ${ctx.args.text}")
    }
}
