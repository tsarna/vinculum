procedure "with_discard" {
    spec {
        params {
            x = required
        }
    }

    _1 = x
    _2 = x
    return = x
}

const {
    result = with_discard(42)
}

assert "discard" {
    condition = (result == 42)
}
