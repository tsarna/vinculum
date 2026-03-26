trigger "interval" "full" {
    initial_delay = "0s"
    delay         = "1h"
    error_delay   = "5m"
    jitter        = 0.1
    stop_when     = ctx.run_count >= 3
    action        = "tick"
}
