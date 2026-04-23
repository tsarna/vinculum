var "temp"           { value = 0 }
var "captured_trace" { value = "" }
var "captured_done"  { value = false }

fsm "hvac" {
    initial = "idle"

    state "idle" {}
    state "cooling" {
        on_entry = [
            set(var.captured_trace, ctx.trace_id),
            set(var.captured_done, true),
        ]
    }

    event "overheat" {
        when = get(var.temp) > 100
        transition "idle" "cooling" {}
    }
}
