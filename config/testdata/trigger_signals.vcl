trigger "signals" "main" {
    SIGUSR1 = log::info("got signal", {signal = ctx.signal})
}
