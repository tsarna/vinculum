const {
    voipms_api_userparam = "api_username=${urlencode(env.VOIPMS_API_USER)}"
    voipms_api_passparam = "api_password=${urlencode(env.VOIPMS_API_PASSWORD)}"
}

client "http" "voipms" {
    base_url = "https://voip.ms/api/v1/rest.php?${voipms_api_userparam}&${voipms_api_passparam}"
}

function "voipms_get_balance" {
    params = [ctx, callstats]
    result = http_must(http_get(ctx, client.voipms, "?method=getBalance", {
        as="json", query={advanced=callstats}
    })).body.balance
}

function "voipms_get_clients" {
    params = [ctx]
    result = http_must(http_get(ctx, client.voipms, "?method=getClients", {
        as="json"
    })).body.clients
}

function "voipms_get_client_threshold" {
    params = [ctx, client_id]
    result = http_must(http_get(ctx, client.voipms, "?method=getClientThreshold", {
        as="json", query={client=client_id}
    })).body.threshold_information
}

function "voipms_get_all_registrations" {
    params = [ctx]
    result = http_must(http_get(ctx, client.voipms, "?method=getRegistrationStatus", {
        as="json"
    })).body.registrations
}

function "voipms_get_reseller_balance" {
    params = [ctx, client_id]
    result = http_must(http_get(ctx, client.voipms, "?method=getResellerBalance", {
        as="json", query={client=client_id}
    })).body.balance
}