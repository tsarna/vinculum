bus "main" {}

var "last" { value = null }

subscription "capture" {
    target   = bus.main
    topics   = ["out/#"]
    disabled = !sys.testing
    action   = set(var.last, ctx.msg)
}

subscription "router" {
    target = bus.main
    topics = ["in/#"]
    action = send(ctx, bus.main, "out/thing", ctx.msg)
}
