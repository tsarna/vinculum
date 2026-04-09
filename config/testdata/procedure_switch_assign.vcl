procedure "bad_switch" {
    spec {
        params {
            x = required
        }
    }

    switch "x" {
        y = 1
        case "1" {
            return = "one"
        }
    }
}
