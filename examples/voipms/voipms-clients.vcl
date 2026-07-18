metric "gauge" "voipms.client.balance" {
    help = "Current VoIP.ms client balance"
    label_names = ["client_id", "client_email"]
}

metric "gauge" "voipms.client.threshold" {
    help = "Current VoIP.ms client balance threshold"
    label_names = ["client_id", "client_email"]
}

metric "counter" "voipms.client.calls" {
    help = "Number of calls handled for this client"
    label_names = ["client_id", "client_email"]
}

# voipms::scrape_client_metrics() lives in voipms-clients.cty (functy).

trigger "interval" "clients" {
    delay         = "1h"
    initial_delay = "1m"
    jitter        = 0.2 # +/- 10%
    action        = voipms::scrape_client_metrics(ctx)
}