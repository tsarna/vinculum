server "metrics" "main" {
    listen = "127.0.0.1:19094"
    path   = "/metrics"
}

# A deliberately tiny window so the end-to-end test can wait it out. Real
# configurations want a small multiple of whatever writes the metric.
metric "gauge" "registered" {
    help        = "Whether an account is registered"
    label_names = ["account"]
    stale_after = "50ms"
}
