condition "timer" "high_temp" {
    activate_after   = "30s"
    deactivate_after = "5m"
    timeout          = "1h"
    cooldown         = "10m"
    debounce         = "50ms"
    latch            = false
    invert           = false
    retentive        = false
}
