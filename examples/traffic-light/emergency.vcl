# Preemption for emergency vehicles

condition "timer" "emergency_preempt" {
    timeout       = "5m" # auto-clear after 5 minutes

    on_init = send(ctx, bus.main, "traffic/emergency/status", false)
    on_activate = send(ctx, bus.main, "traffic/emergency/status", true)
    on_deactivate = send(ctx, bus.main, "traffic/emergency/status", false)
}

subscription "emergency" {
    target = bus.main
    topics = ["traffic/emergency/set"]
    action = set(condition.emergency_preempt, ctx.msg)
}
