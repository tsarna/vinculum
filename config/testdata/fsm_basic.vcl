fsm "door" {
    initial = "closed"

    storage {
        open_count = 0
        last_user  = "unknown"
    }

    state "closed" {}

    state "open" {}

    state "locked" {}

    event "open" {
        transition "closed" "open" {}
    }

    event "close" {
        transition "open" "closed" {}
    }

    event "lock" {
        transition "closed" "locked" {}
    }

    event "unlock" {
        transition "locked" "closed" {}
    }

    event "emergency" {
        transition "*" "closed" {}
    }
}
