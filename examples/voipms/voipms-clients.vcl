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

procedure "voipms_scrape_client_metrics" {
    spec {
        params {
            ctx = required
        }
    }

    clients = voipms_get_clients(ctx)

    range "client" "clients" {
        labels = { client_id = client.client, client_email = client.email }
        balance = voipms_get_reseller_balance(ctx, client.client)
        thresh = voipms_get_client_threshold(ctx, client.client)
        _ = [
            log_debug("clients", {labels=labels, balance=balance, thresh=thresh}), 
            set(metric.voipms_client_balance, tonumber(balance.current_balance), labels),
            set(metric.voipms_client_threshold, tonumber(thresh.threshold), labels),
            set(metric.voipms_client_calls, balance.calls_total, labels)
        ]
    }
}

trigger "interval" "clients" {   
    delay         = "1h"
    initial_delay = "1m"
    jitter        = 0.2 # +/- 10%
    action        = voipms_scrape_client_metrics(ctx)
}