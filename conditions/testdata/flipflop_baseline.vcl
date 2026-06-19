# A toggle source that is already asserting at boot must NOT toggle the
# flipflop — the first evaluation only establishes the per-wire baseline.

var "boot_high" { value = true }
condition "flipflop" "lamp" {
    toggle_on = get(var.boot_high)
}
