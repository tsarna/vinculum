# invert applies a final inversion to the output. With start_active + invert,
# get() reports false at boot.
var "iv_tog" { value = false }
condition "flipflop" "inv" {
    toggle_on    = get(var.iv_tog)
    start_active = true
    invert       = true
}
