client "otlp" "primary" {
    endpoint     = "http://localhost:4318"
    service_name = "test"
}

client "otlp" "secondary" {
    endpoint     = "http://other:4318"
    service_name = "test"
}
