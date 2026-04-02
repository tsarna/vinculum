client "otlp" "tracer" {
    endpoint     = "http://localhost:4318"
    service_name = "test"
    default      = true
}

server "http" "main" {
    listen  = "127.0.0.1:18080"
    tracing = client.tracer

    handle "/hello" {
        action = "Hello, World!"
    }
}
