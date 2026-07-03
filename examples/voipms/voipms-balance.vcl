metric "gauge" "voipms.balance" {
    help = "Current VoIP.ms reseller balance"
}

metric "counter" "voipms.calls" {
    help = "Number of calls handled in this account"
}

metric "counter" "voipms.spent" {
    help = "Total spent by reseller on VoIP.ms"
}

# voipms_scrape_balance_metrics() lives in voipms-balance.cty (functy).

trigger "interval" "balance" {
    delay         = "1h"
    initial_delay = "1m"
    jitter        = 0.2 # +/- 10%
    action        = voipms_scrape_balance_metrics(ctx)
}