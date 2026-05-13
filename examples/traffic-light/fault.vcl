# Fault detection and management

# master fault
condition "timer" "master_fault" {
    latch = true # require explicit clear

    input = (get(condition.power_outage) || get(condition.manual_fault)
                || get(condition.conflict) || get(condition.timeout))

    on_init = send(ctx, bus.main, "traffic/fault/status/master", true)
    on_activate = send(ctx, bus.main, "traffic/fault/status/master", true)
    on_deactivate = send(ctx, bus.main, "traffic/fault/status/master", false)
}

# start in power outage state
condition "timer" "power_outage" {
    start_active  = true
    latch = true

    on_init = send(ctx, bus.main, "traffic/fault/status/power_outage", true)
    on_activate = send(ctx, bus.main, "traffic/fault/status/power_outage", true)
    on_deactivate = send(ctx, bus.main, "traffic/fault/status/power_outage", false)
}

# manual fault condition, e.g. for maintenance
condition "timer" "manual_fault" {
    on_init = send(ctx, bus.main, "traffic/fault/status/manual", true)
    on_activate = send(ctx, bus.main, "traffic/fault/status/manual", true)
    on_deactivate = send(ctx, bus.main, "traffic/fault/status/manual", false)
}

function "allows_traffic" {
    params = [ light ]
    result = (get(light) == "GREEN" || get(light) == "YELLOW")
}

# detect when crossing directions have green/yellow lights, allowing colliding traffic
condition "timer" "conflict" {
    latch         = true    # require explicit clear, even if condition no longer holds
    debounce      = "250ms" # require conflict condition to persist for 250ms before activating

    input = ((allows_traffic(var.north_light) || allows_traffic(var.south_light))
          && (allows_traffic(var.east_light)  || allows_traffic(var.west_light)))

    on_init = send(ctx, bus.main, "traffic/fault/status/conflict", true)
    on_activate = send(ctx, bus.main, "traffic/fault/status/conflict", true)
    on_deactivate = send(ctx, bus.main, "traffic/fault/status/conflict", false)
}

# Simulate flaky behavior

#trigger "interval" "fault" {
#    delay = "5m"
#    jitter = 0.30
#    # This will sometimes result in green/yellow lights in two directions at once, which should trigger the fault condition in the FSM
#    action = [ log_info("simulating fault"), send(ctx, bus.main, "traffic/lights/ns", "GREEN")]
#}

# trigger manual fault on command
subscription "trigger_fault" {
    target = bus.main
    topics = ["traffic/fault/trigger/manual"]
    action = set(condition.manual_fault, ctx.msg)
}

# clear fault on command
subscription "clear_fault" {
    target = bus.main
    topics = ["traffic/fault/reset/+name"]
    action = clear(switch(ctx.fields.name,
        "master", condition.master_fault,
        "power_outage", condition.power_outage,
        "manual", condition.manual_fault,
        "conflict", condition.conflict,
        "timeout", condition.timeout))
}

# Timeout if the FSM gets stuck in a state for too long, unless in manual mode

condition "timer" "timeout" {
    latch = true

    on_init = send(ctx, bus.main, "traffic/fault/status/timeout", false)
    on_activate = send(ctx, bus.main, "traffic/fault/status/timeout", true)
    on_deactivate = send(ctx, bus.main, "traffic/fault/status/timeout", false)
}

trigger "watchdog" "fsm_timeout" {
    window = "5m"
    watch = fsm.intersection
    action = cond(condition.manual_phases == false && condition.emergency_preempt == false,
        set(condition.timeout, true), null)
}
