server "http" "main" {
    listen = "127.0.0.1:18081"

    handle "/metrics" {
        handler = server.metrics
    }
}

server "metrics" "metrics" {
    # no listen = mounted mode only
}

metric "gauge" "uptime" {
    help = "Uptime seconds"
}
