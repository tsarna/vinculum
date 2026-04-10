# if a "main" bus is not defined, one is define automatically as if this was present:
# bus "main" {}

assert "main bus" {
    condition = (bus.main != null)
}

bus "ws" {
    queue_size = 100
}

const {
    logged = log_warn("@@@ warn", 1, 2.5, "string", [1,2,3])
    logged1 = log_info("@@@ info", {foo="hello", bar=42, baz=7.3, qux=[1,2,3]})
    logged2 = log_msg("error", "@@@ error", {foo="hello", bar=42, baz=7.3, qux=[1,2,3]})
}

assert "ws bus" {
    condition = (bus.ws != null)
}

