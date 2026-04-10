trigger "signals" "main" {
    SIGUSR1 = log_info("got signal", {signal = ctx.signal})
}
