const {
    # If configured, use otlp, else expose metrics via prometheus

    otlp_endpoint = try(env.OTLP_URL, "")
}

server "metrics" "prometheus" {
    listen = ":9090"

    disabled = (otlp_endpoint != "")
}

client "otlp" "otlp" {
    endpoint     = otlp_endpoint
    service_name = "vinculum-voipms"

    disabled = (otlp_endpoint == "")
}
