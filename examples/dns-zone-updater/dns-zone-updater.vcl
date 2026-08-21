# Dynamic DNS zone file updater
#
# Endpoints, deployment, the credentials file format, and the Unifi Network
# Controller settings are all documented in README.md next to this file.
#
# Nothing site-specific lives in this file: credentials come from a mounted
# secret and the rest from the environment, so this config can be published,
# pinned, and fetched by a .vinit git block as-is.

const {
    # Credentials are a JSON object mapping "{zone}/{host}" to that
    # credential's password:
    #
    #   { "dyn.example.com/foo": "s3cret", "dyn.example.com/bar": "hunter2" }
    #
    # Adding or revoking a credential is a change to the secret, not to this
    # file. It is read once at startup, so a missing or malformed file fails
    # the boot rather than each individual request.
    #
    # The path is relative to --file-path unless absolute; file() is not
    # defined at all unless --file-path is set.
    #
    # A const is referenced by its bare name, not as const.<name>.
    dns_updaters = jsondecode(file(try(env.DNS_CREDENTIALS_FILE, "dns-updaters.json")))
}

auth "basic" "updaters" {
    realm = "Dynamic DNS"

    # A username the file does not list is rejected, so an empty or absent
    # credential set denies everything. Do NOT add `disabled` here to make the
    # secret optional: a route whose only mechanism is disabled becomes
    # unauthenticated. See doc/auth.md, "Turning a mechanism off".
    credentials = dns_updaters
}

server "http" "dns_webhook" {
    listen = try(env.DNS_LISTEN, ":8080")
    auth   = auth.updaters

    # A query parameter that wasn't sent is not a key of ctx.request.form at
    # all, so indexing it directly would fail to evaluate and return a 500.
    # try() turns that into "", which update_dns answers with a 400.
    handle "GET /dns/update/{zone}" {
        action = update_dns(ctx, ctx.auth.username, ctx.request.path.zone,
            try(ctx.request.form.host[0], ""), try(ctx.request.form.ip[0], ""), false)
    }

    handle "GET /dns/disable/{zone}" {
        action = update_dns(ctx, ctx.auth.username, ctx.request.path.zone,
            try(ctx.request.form.host[0], ""), "127.0.0.1", true)
    }
}

# The update_dns() function called by the handlers above lives in
# dns-zone-updater.cty (functy). Any .cty file in this directory shares the same
# namespace as the .vcl files, so functy functions are callable from VCL
# expressions just like built-ins.

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
        replace = "${ctx.groups[0]} ${sys.hostname} on ${time::format("@rfc3339", time::now("UTC"))}\n"
        update_state = {
            saw_header = true
        }
    }

    # Prepend the header if there wasn't one
    # (runs AFTER all lines are processed; state.saw_header is final)
    before {
        incidental = true
        content = state.saw_header ? "" : ";;; Updated by ${sys.hostname} on ${time::format("@rfc3339", time::now("UTC"))}\n\n"
    }

    # Update the SOA serial: matches "        2024010101 ; Serial"
    match "^(\\s*)(\\d+)(\\s*;\\s*[Ss]erial)" {
        required = true
        incidental = true
        replace  = "${ctx.groups[1]}${dns::next_zone_serial(ctx.groups[2])}${ctx.groups[3]}\n"
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
