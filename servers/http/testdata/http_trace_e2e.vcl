server "http" "main" {
    listen = "127.0.0.1:0"

    handle "/trace" {
        action = {trace_id = ctx.trace_id, span_id = ctx.span_id}
    }
}
