procedure "all_branches_return" {
    spec {
        params {
            x = null
        }
    }

    if "x > 0" {
        return = "yes"
    }
    else {
        return = "no"
    }

    x = 1
}
