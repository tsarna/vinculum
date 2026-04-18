# Dynamic DNS zone file updater

# GET /dns/update/{zone}?host=foo&ip=1.2.3.4
#     updates zone record A for foo, re-enabling if disabled
# GET /dns/disable/{zone}?host=foo
#     disables the A record for foo by commenting it out

# Compatible with Unifi Network Controller's Dynamic DNS feature, configured as follows:
#
# Service: Custom
# Hostname: foo.dyn.example.com
# Username: dyn.example.com/foo
# Password: ...
# Server: api.example.com/dns/update/dyn.example.com?host=foo&ip=%i

# Set ZONES_DIR to the directory containing the zone files, e.g. /etc/bind/zones
# The directory should contain a zone file for each zone, named {zone}.zone, e.g. dyn.example.com.zone

server "http" "dns_webhook" {
    listen = ":8080"

    auth "basic" {
        credentials = {
            # Credential usernames should be in the form "{zone}/{host}", e.g. "dyn.example.com/foo"

            "dyn.example.com/foo" = env.PASS_FOO_DYN_EXAMPLE_COM
        }
    }

    handle "GET /dns/update/{zone}" {
        action = update_dns(ctx, ctx.auth.username, ctx.request.path.zone,
            ctx.request.form.host[0], ctx.request.form.ip[0], false)
    }

    handle "GET /dns/disable/{zone}" {
        action = update_dns(ctx, ctx.auth.username, ctx.request.path.zone,
            ctx.request.form.host[0], "127.0.0.1", true)
    }
}

procedure "update_dns" {
    spec {
        params {
            ctx = required
            username = required
            zone = required
            host = required
            ip = required  
            disabled = required
        }
    }
    
    expected_username = "${zone}/${host}"
    if "username != expected_username || ip == \"\"" {
        return = http_response(http_status.Forbidden, "Forbidden")
    }

    file_changed = update_zone_record(ctx, "${env.ZONES_DIR}/${zone}.zone", host, ip, disabled)
    if "file_changed" {
        # You could add other logic here, eg publish a message to MQTT, send an SMS, etc.

        _ = log_info("Updated DNS record", {
            host = "${host}.${zone}", new_ip=ip, enabled = !disabled
        })

        return = http_response(http_status.OK, "good ${ip}")
    }

    return = http_response(http_status.OK, "nochg ${ip}")
}
 
# A line editor is a more declarative way of specifying a function to update a file line-by-line
# This creates the function update_zone_record(filepath, recordname, ipaddr, disabled)

editor "line" "update_zone_record" {
    params = [recordname, ipaddr, disabled]
    lock = true # Use a lock file to prevent simultaneous edits to the same file

    state = {
        # initial state for the file; can be read and updated by the match and before blocks
        saw_header = false
    }

    # Header update
    match "^(;;;\\s*Updated by)" {
        # incidental means don't consider this edit to count as a file change
        # if the only edits are incidental, the updated file will be discarded and the original
        # will be left in place.

        # This allows edists such as updating a timestamp comment or the zone serial only
        # when the file is changed due to an actual record update.

        incidental = true
        replace = "${ctx.groups[0]} ${sys.hostname} on ${formattime("@rfc3339", now("UTC"))}\n"
        update_state = {
            saw_header = true
        }
    }

    # Prepend the header if there wasn't one
    # (runs AFTER all lines are processed; state.saw_header is final)
    before {
        incidental = true
        content = state.saw_header ? "" : ";;; Updated by ${sys.hostname} on ${formattime("@rfc3339", now("UTC"))}\n\n"
    }

    # Update the SOA serial: matches "        2024010101 ; Serial"
    match "^(\\s*)(\\d+)(\\s*;\\s*[Ss]erial)" {
        required = true
        incidental = true
        replace  = "${ctx.groups[1]}${nextzoneserial(ctx.groups[2])}${ctx.groups[3]}\n"
    }

    # Replace the A record for the named host: matches "www    IN A    1.2.3.4"
    # The regex matches any A record; when = ... filters to just the target host.
    # Adds or removes a ;DISABLED; prefix
    match "^(;DISABLED;)?(\\S+)(\\s+(?:IN\\s+)?A\\s+)\\S+" {
        required = true
        when     = ctx.groups[2] == recordname
        replace  = "${disabled ? ";DISABLED;" : ""}${ctx.groups[2]}${ctx.groups[3]}${ipaddr}\n"
    }
}
