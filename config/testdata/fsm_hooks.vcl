var "log" {
    value = ""
}

fsm "door" {
    initial = "closed"

    storage {
        open_count = 0
    }

    state "closed" {
        on_init  = set(var.log, "init")
        on_entry = set(var.log, "entry:closed")
        on_exit  = set(var.log, "exit:closed")
    }

    state "open" {
        on_entry = increment(fsm.door, "open_count")
        on_event = set(var.log, "on_event:open")
    }

    state "locked" {}

    event "open" {
        transition "closed" "open" {
            action = set(var.log, "action:open")
        }
    }

    event "close" {
        transition "open" "closed" {}
    }

    event "lock" {
        transition "closed" "locked" {
            guard = state(fsm.door) == "closed"
        }
    }

    on_change = set(var.log, "changed")
}
