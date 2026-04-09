procedure "bad" {
    spec {
        params {
            x = required
        }
        variadic_param = x
    }
    return = x
}
