server "http" "traffic" {
    listen = ":8080"

    files "/traffic" {
        directory = "/conf/html"
    }

    handle "GET /{$}" {
        action = http_redirect("/traffic/")
    }

    handle "/vws" {
        handler = server.vws
    }

    handle "GET /api/traffic/status" {
        action = {
            state     = state(fsm.intersection)
            lights    = {
                north = get(var.north_light)
                east  = get(var.east_light)
                south = get(var.south_light)
                west  = get(var.west_light)
            }
            mode             = get(var.mode)
            emergency        = get(condition.emergency_preempt)
            manual_phases    = get(condition.manual_phases)
            faults    = {
                master       = get(condition.master_fault)
                power_outage = get(condition.power_outage)
                manual       = get(condition.manual_fault)
                conflict     = get(condition.conflict)
                timeout      = get(condition.timeout)
            }
        }
    }
}

server "vws" "vws" {
    bus = bus.main
    allow_send = true
}
