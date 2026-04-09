procedure "bad_switch" {
    spec {
        params {
            x = required
        }
    }

    switch "x" {
        default {
            return = 1
        }
        default {
            return = 2
        }
    }
}
