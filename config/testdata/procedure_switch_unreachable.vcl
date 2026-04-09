procedure "bad" {
    spec {
        params {
            x = required
        }
    }

    switch "x" {
        case "1" { return = "one" }
        default { return = "other" }
    }

    x = 99
}
