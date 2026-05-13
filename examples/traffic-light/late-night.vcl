# Late night mode: blinking yellow in EW direction, red in NS direction

var "mode" { value = "NORMAL" }

subscription "mode" {
    target = bus.main
    topics = ["traffic/mode"]
    action = set(var.mode, ctx.msg)
}

trigger "cron" "latenight_mode" {
    # midnight to 6am is "late night mode" with different blinking behavior

    at "0 0 * * *" "late_night" {
        action = send(ctx, bus.main, "traffic/mode", "LATE_NIGHT")
    }

    at "0 6 * * *" "normal" {
        action = send(ctx, bus.main, "traffic/mode", "NORMAL")
    }
}

trigger "start" "set_initial_mode" {
    action = set(var.mode, cond(get(now(), "hour") >= 0 && get(now(), "hour") < 6, "LATE_NIGHT", "NORMAL"))
}
