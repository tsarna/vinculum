trigger "file" "full" {
    path              = "/tmp"
    events            = ["create", "write"]
    recursive         = true
    filter            = "*.json"
    debounce          = "200ms"
    on_start_existing = true
    skip_when         = false
    action            = "ok"
}
