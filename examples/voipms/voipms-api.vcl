const {
    voipms_api_userparam = "api_username=${urlencode(env.VOIPMS_API_USER)}"
    voipms_api_passparam = "api_password=${urlencode(env.VOIPMS_API_PASSWORD)}"
}

client "http" "voipms" {
    base_url = "https://voip.ms/api/v1/rest.php?${voipms_api_userparam}&${voipms_api_passparam}"
}

# The API wrapper functions that call client.voipms live in voipms-api.cty
# (functy). A .cty file shares the same namespace as the .vcl files in this
# directory, so those functions are callable from any VCL expression.