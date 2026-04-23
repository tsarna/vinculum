bus "main" {}

condition "timer" "fault" {
    latch         = true
    start_active  = true
    on_init       = send(ctx, bus.main, "hook", {kind = "on_init", new = ctx.new_value})
    on_activate   = send(ctx, bus.main, "hook", {kind = "on_activate", new = ctx.new_value, old = ctx.old_value})
    on_deactivate = send(ctx, bus.main, "hook", {kind = "on_deactivate", new = ctx.new_value, old = ctx.old_value})
}
