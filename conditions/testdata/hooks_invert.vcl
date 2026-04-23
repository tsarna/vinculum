bus "main" {}

condition "timer" "gate" {
    invert        = true
    on_activate   = send(ctx, bus.main, "hook", {kind = "on_activate", new = ctx.new_value, old = ctx.old_value})
    on_deactivate = send(ctx, bus.main, "hook", {kind = "on_deactivate", new = ctx.new_value, old = ctx.old_value})
}
