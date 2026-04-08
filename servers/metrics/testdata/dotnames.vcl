server "metrics" "main" {
    listen = "127.0.0.1:19091"
}

metric "gauge" "http.server.active_requests" {
    help = "Active HTTP requests"
}

metric "counter" "app.requests_total" {
    help      = "Total application requests"
    namespace = "my.app"
}
