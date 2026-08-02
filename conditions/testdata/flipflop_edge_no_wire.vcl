# Error: set_edge selects which edge of set_on fires, and there is no set_on.
# Silently ignoring it would let a typo, or a wire deleted without its edge,
# look configured while doing nothing.
var "s" { value = false }
condition "flipflop" "orphan_edge" {
    toggle_on = get(var.s)
    set_edge  = "falling"
}
