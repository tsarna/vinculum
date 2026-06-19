# While inhibit is true, a wire-driven activation is suppressed. A flipflop
# already active is unaffected.
var "ih_set" { value = false }
var "ih_reset" { value = false }
var "ih_block" { value = false }
condition "flipflop" "guarded" {
    set_on   = get(var.ih_set)
    reset_on = get(var.ih_reset)
    inhibit  = get(var.ih_block)
}
