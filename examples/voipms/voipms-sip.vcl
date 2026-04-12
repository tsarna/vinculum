metric "gauge" "voipms.sipaccount.registered" {
    help = "Is a device currently registered on the SIP account"
    label_names = ["account_id"]
}

procedure "voipms_scrape_sip_accounts" {
    spec {
        params {
            ctx = required
        }
    }

    registrations = voipms_get_all_registrations(ctx)
    range "reg" "registrations" {
        labels = { account_id = reg.account }
        _ = [
            log_info("sip_account", {labels=labels}),
            set(metric.voipms_sipaccount_registered, 1, labels),
        ]
    }
}

trigger "interval" "sipaccounts" {
    delay         = "4m"
    initial_delay = "5s"
    jitter        = 0.2 # +/- 10%
    action        = voipms_scrape_sip_accounts(ctx)
}