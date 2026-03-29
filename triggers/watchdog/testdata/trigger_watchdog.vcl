trigger "watchdog" "heartbeat" {
    window = "1h"
    action = "missed"
}
