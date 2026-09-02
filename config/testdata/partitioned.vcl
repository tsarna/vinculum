// A partitioned subscription over the things an action can reach that hold
// state: a var it reads and writes, and a second bus it sends on. Run under
// -race, it is what says the work of eight goroutines is safe to do at once.
//
// No condition block: condition types register from the `conditions` package,
// which the config package's tests do not import. The audit that preceded this
// found all five subtypes hold a sync.Mutex, and each has its own tests.

bus "main" {}
bus "handled" {}

var "seen" { value = 0 }

subscription "work" {
    target        = bus.main
    topics        = ["work/+device"]
    queue_size    = 100
    partitions    = 8
    partition_key = ctx.fields.device

    action = [
        set(ctx, var.seen, get(var.seen) + 1),
        send(ctx, bus.handled, "handled/${ctx.fields.device}", ctx.msg),
    ]
}
