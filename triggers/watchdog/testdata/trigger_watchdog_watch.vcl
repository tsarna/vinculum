var "signal" {}

trigger "watchdog" "dog" {
    window = "1h"
    watch  = var.signal
    action = "missed"
}
