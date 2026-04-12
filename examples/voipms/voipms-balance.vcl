metric "gauge" "voipms.balance" {
    help = "Current VoIP.ms reseller balance"
}

metric "counter" "voipms.calls" {
    help = "Number of calls handled in this account"
}

metric "counter" "voipms.spent" {
    help = "Total spent by reseller on VoIP.ms"
}

procedure "voipms_scrape_balance_metrics" {
    spec {
        params {
            ctx = required
        }
    }

    balance = voipms_get_balance(ctx, true)
    _ = [
        log_debug("balance", {balance=balance}),
        set(metric.voipms_balance, tonumber(balance.current_balance)),
        set(metric.voipms_calls, balance.calls_total),
        set(metric.voipms_spent, floor(tonumber(balance.spent_total)))
    ]
}

trigger "interval" "balance" {
    delay         = "1h"
    initial_delay = "1m"
    jitter        = 0.2 # +/- 10%
    action        = voipms_scrape_balance_metrics(ctx)
}