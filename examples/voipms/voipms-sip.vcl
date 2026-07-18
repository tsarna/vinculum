metric "gauge" "voipms.sipaccount.registered" {
    help = "Is a device currently registered on the SIP account"
    label_names = ["account_id"]
}

# voipms::scrape_sip_accounts() lives in voipms-sip.cty (functy).

trigger "interval" "sipaccounts" {
    delay         = "4m"
    initial_delay = "5s"
    jitter        = 0.2 # +/- 10%
    action        = voipms::scrape_sip_accounts(ctx)
}