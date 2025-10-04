assert "assertion name" {
    condition = true # any expression that evaluates to true, or if not the assertion fails
}

bus "main" {
    # if not declared, the "main" bus is createda automatically
    queue_size = 1000 # default
}

const {
    pi = 3.141529
}

cron "main" {
    at "* * * * *" "tick" {
        action = send(ctx, bus.main, "test/tick", "testing 123")
    }
}

function "circle_area" {
    params = [r]
    result = pi * r * r
}

jq "functionname" {
    params = [r]
    query = "{.name}"
}

subscription "test1" {
    bus = bus.main
    topics = ["test/tick"]
    action = send(ctx, bus.main, "test/tock", "@@@ info from ${ctx.topic}")
}

# New functions:

# diff(a, b)
#    return a map for patching a to match b
# log_debug("message", [any...])
# log_error("message", [any...])
# log_info("message", [any...])
# log_level("level", "message", [any...])
# log_warn("message", [any...])
# patch(target, patch)
#     apply the patch to the target and return the result
# send(context, bus_or_subscriber, "topic", anything, [fields])
#     send a message (anything) on a bus or directly to a subscriber. If supplied, fields
#     should be a map of strings to strings
# sendgo(context, bus_or_subscriber, "topic", anything, [fields])
#     Like send(), but sends message as Go primitive objects. This could be of use
#     when vinculum is embedded in a go program.
# sendjson(context, bus_or_subscriber, "topic", anything, [fields])
#     Like send(), but comverts message to JSON and sends that.
# typeof(x)
#     returns a string with the friendly name of the type of x 