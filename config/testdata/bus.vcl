# A bus named "main" is a bus like any other: it exists because it is declared
# here, and a configuration that does not declare one does not have one.
bus "main" {}

assert "main bus" {
    condition = (bus.main != null)
}

bus "ws" {
    queue_size = 100
}

const {
    logged = log::warn("@@@ warn", 1, 2.5, "string", [1,2,3])
    logged1 = log::info("@@@ info", {foo="hello", bar=42, baz=7.3, qux=[1,2,3]})
    logged2 = log::msg("error", "@@@ error", {foo="hello", bar=42, baz=7.3, qux=[1,2,3]})
}

assert "ws bus" {
    condition = (bus.ws != null)
}

