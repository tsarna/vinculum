server "metrics" "main" {
    listen = "127.0.0.1:19090"
    path   = "/metrics"
}

metric "gauge" "temperature" {
    help = "Current temperature"
}

metric "counter" "requests" {
    help = "Total requests"
}

metric "histogram" "latency" {
    help    = "Request latency"
    buckets = [0.01, 0.05, 0.1, 0.5, 1.0]
}
