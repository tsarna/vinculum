client "otlp" "tracer" {
    endpoint        = "https://collector.example.com:4318"
    service_name    = "my-app"
    service_version = "2.0.0"
    sampling_ratio  = 0.5
    default         = true
}
