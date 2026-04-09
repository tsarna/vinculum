procedure "sum_all" {
    spec {
        params {
            first = null
        }
        variadic_param = rest
    }

    total = first
    return = total
}

const {
    one = sum_all(10)
}

assert "one" {
    condition = (one == 10)
}
