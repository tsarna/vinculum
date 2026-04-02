trigger "file" "spool" {
    path              = "/tmp"
    events            = ["create"]
    on_start_existing = true
    action            = "ok"
}
