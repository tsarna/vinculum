server "http" "traffic" {
    listen = ":8080"

    # Recover the real client IP when running behind a reverse proxy. Driven by a
    # single env var: set TRAFFIC_TRUSTED_PROXIES to a comma-separated list of
    # proxy CIDRs/IPs to enable; leave it unset to disable.
    real_ip {
        disabled        = try(env.TRAFFIC_TRUSTED_PROXIES, "") == ""
        trusted_proxies = compact(split(",", try(env.TRAFFIC_TRUSTED_PROXIES, "")))
    }

    # Optional HTTP basic auth over the whole server. Set TRAFFIC_WEB_PASSWORD to
    # require a login (username from TRAFFIC_WEB_USER, default "admin"); leave the
    # password unset to disable auth entirely.
    auth "basic" {
        disabled    = try(env.TRAFFIC_WEB_PASSWORD, "") == ""
        realm       = "Traffic Light"
        credentials = { (try(env.TRAFFIC_WEB_USER, "admin")) = try(env.TRAFFIC_WEB_PASSWORD, "") }
    }

    files "/traffic" {
        directory = try(env.TRAFFIC_HTML_DIR, "/conf/html")
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
