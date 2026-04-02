client "otlp" "primary" {
    endpoint     = "http://localhost:4318"
    service_name = "test"
    default      = true
}

client "otlp" "secondary" {
    endpoint     = "http://other:4318"
    service_name = "test"
}
