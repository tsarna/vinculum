bus "main" {}

var "north_light" { value = "RED" }
var "east_light" { value = "RED" }
var "south_light" { value = "RED" }
var "west_light" { value = "RED" }

subscription "lights" {
    target = bus.main
    topics = ["traffic/lights/+direction"]
    action = set(switch(ctx.fields.direction,
        "north", var.north_light,
        "east", var.east_light,
        "south", var.south_light,
        "west", var.west_light), ctx.msg)
}

subscription "fsm" {
    target     = bus.main
    topics     = ["traffic/#"]
    subscriber = fsm.intersection
}

fsm "intersection" {
    initial = "fourway_flash_on"

    storage {
        last_green = "ns_light"
    }

    on_change = send(ctx, bus.main, "traffic/state", ctx.new_state)

    state "fourway_flash_on" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "RED"),
            send(ctx, bus.main, "traffic/lights/east", "RED"),
            send(ctx, bus.main, "traffic/lights/south", "RED"),
            send(ctx, bus.main, "traffic/lights/west", "RED"),
        ]
    }

    state "fourway_flash_off" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "OFF"),
            send(ctx, bus.main, "traffic/lights/east", "OFF"),
            send(ctx, bus.main, "traffic/lights/south", "OFF"),
            send(ctx, bus.main, "traffic/lights/west", "OFF"),
        ]
    }

    state "clearance" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "RED"),
            send(ctx, bus.main, "traffic/lights/east", "RED"),
            send(ctx, bus.main, "traffic/lights/south", "RED"),
            send(ctx, bus.main, "traffic/lights/west", "RED"),
            set(trigger.phase_change, "2s"),
        ]
    }

    state "ns_green" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "GREEN"),
            send(ctx, bus.main, "traffic/lights/east", "RED"),
            send(ctx, bus.main, "traffic/lights/south", "GREEN"),
            send(ctx, bus.main, "traffic/lights/west", "RED"),
            set(trigger.phase_change, "10s"),
        ]
    }

    state "ns_yellow" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "YELLOW"),
            send(ctx, bus.main, "traffic/lights/east", "RED"),
            send(ctx, bus.main, "traffic/lights/south", "YELLOW"),
            send(ctx, bus.main, "traffic/lights/west", "RED"),
            set(trigger.phase_change, "5s"),
        ]
    }

    state "ew_green" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "RED"),
            send(ctx, bus.main, "traffic/lights/east", "GREEN"),
            send(ctx, bus.main, "traffic/lights/south", "RED"),
            send(ctx, bus.main, "traffic/lights/west", "GREEN"),
            set(trigger.phase_change, "10s"),
        ]
    }

    state "ew_yellow" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "RED"),
            send(ctx, bus.main, "traffic/lights/east", "YELLOW"),
            send(ctx, bus.main, "traffic/lights/south", "RED"),
            send(ctx, bus.main, "traffic/lights/west", "YELLOW"),
            set(trigger.phase_change, "5s"),
        ]
    }
    
    state "latenight_flash_on" {
        on_entry = [
        send(ctx, bus.main, "traffic/lights/north", "RED"),
            send(ctx, bus.main, "traffic/lights/east", "YELLOW"),
            send(ctx, bus.main, "traffic/lights/south", "RED"),
            send(ctx, bus.main, "traffic/lights/west", "YELLOW"),
        ]
    }

    state "latenight_flash_off" {
        on_entry = [
            send(ctx, bus.main, "traffic/lights/north", "OFF"),
            send(ctx, bus.main, "traffic/lights/east", "OFF"),
            send(ctx, bus.main, "traffic/lights/south", "OFF"),
            send(ctx, bus.main, "traffic/lights/west", "OFF"),
        ]
    }

    event "next_phase" {
        topic = "traffic/next_phase"

        transition "clearance" "latenight_flash_off" {
            guard = (get(var.mode) == "LATE_NIGHT") && !get(condition.emergency_preempt)
        }

        transition "clearance" "ns_green" {
            guard = (get(ctx.fsm, "last_green") == "ew_light") && !get(condition.emergency_preempt)
        }

        transition "clearance" "ew_green" {
            guard = (get(ctx.fsm, "last_green") == "ns_light") && !get(condition.emergency_preempt)
        }

        transition "ns_green" "ns_yellow" {
            action = set(ctx.fsm, "last_green", "ns_light")
        }

        transition "ew_green" "ew_yellow" {
            action = set(ctx.fsm, "last_green", "ew_light")
        }

        transition "ns_yellow" "clearance" {}

        transition "ew_yellow" "clearance" {}
    }

    event "fault_detected" {
        when = get(condition.master_fault)

        transition "*" "fourway_flash_on" {}
    }

    event "fault_cleared" {
        when = !get(condition.master_fault)

        transition "*" "clearance" {}
    }

    # immediately go yellow, then enter clearance as normal
    event "emergency_preempt" {
        when = get(condition.emergency_preempt)
        
        transition "ew_green" "ew_yellow" {}
        transition "ns_green" "ns_yellow" {}

        transition "latenight_flash_on" "ew_yellow" {}
        transition "latenight_flash_off" "ew_yellow" {}
    }

    # resume normal operation, starting with clearance
    event "emergency_cleared" {
        when = !get(condition.emergency_preempt)

        # If we never made it out of yellow, advance to clearance
        transition "ns_yellow" "clearance" {}
        transition "ew_yellow" "clearance" {}

        # if we're already in clearance, just reset the timer
        transition  "clearance" "clearance" {} 
    }

    event "blink" {
        transition "fourway_flash_off" "fourway_flash_on" {}
        transition "fourway_flash_on" "fourway_flash_off" {}

        transition "latenight_flash_off" "latenight_flash_on" {}

        transition "latenight_flash_on" "latenight_flash_off" {
            guard = (get(var.mode) == "LATE_NIGHT")
        }

        transition "latenight_flash_on" "ew_green" {
            guard = (get(var.mode) == "NORMAL")
        }
    }
}

trigger "interval" "phase_change" {
    repeat = false

    # Don't automatically advance phases if we're in manual mode
    action = cond(get(condition.manual_phases), null, 
        send(ctx, fsm.intersection, "traffic/next_phase", {}))
}

trigger "interval" "blink" {
    delay = "500ms"
    action = send(ctx, fsm.intersection, "blink", {})
}