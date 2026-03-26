trigger "signals" "main" {
    SIGUSR1 = loginfo("got signal", {signal = ctx.signal})
}
