# Manual mode. In real life, some intersections can be put into this mode
# where a police officer or construction worker manually controls the lights
# depending on traffic conditions. Usually there is a pendant with a button
# that is pressed to trigger a phase change, and the lights will stay

condition "timer" "manual_phases" {
    timeout       = "3h" # auto-clear after 3 hours

    on_init = send(ctx, bus.main, "traffic/manual_phases/status", false)

    on_activate = [
        send(ctx, bus.main, "traffic/mode", "NORMAL"),
        send(ctx, bus.main, "traffic/manual_phases/status", true)
    ]

    on_deactivate = [
        send(ctx, bus.main, "traffic/manual_phases/status", false),
        send(ctx, bus.main, "traffic/next_phase", {}) # trigger phase change to reset timer
    ]
}

subscription "manual_mode" {
    target = bus.main
    topics = ["traffic/manual_phases/set"]
    action = set(condition.manual_phases, ctx.msg)
}
