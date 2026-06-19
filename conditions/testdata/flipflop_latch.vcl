# A latched flipflop ignores reset edges and toggle-down once active; clear()
# releases the latch.
var "lt_set" { value = false }
var "lt_reset" { value = false }
var "lt_tog" { value = false }
condition "flipflop" "sticky" {
    set_on    = get(var.lt_set)
    reset_on  = get(var.lt_reset)
    toggle_on = get(var.lt_tog)
    latch     = true
}
