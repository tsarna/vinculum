var "temperature" {
    value = 50
}

fsm "hvac" {
    initial = "idle"

    state "idle" {}
    state "cooling" {}

    event "overheat" {
        when = get(var.temperature) > 100

        transition "idle" "cooling" {}
    }

    event "cool_down" {
        when = get(var.temperature) <= 80

        transition "cooling" "idle" {}
    }

    # This event has both when and topic, so it participates in both.
    event "manual_cool" {
        topic = "hvac/cool"

        transition "idle" "cooling" {}
    }
}
