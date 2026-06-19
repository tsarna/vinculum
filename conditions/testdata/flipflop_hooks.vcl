# on_activate / on_deactivate fire on output transitions; on_init fires once at
# PostStart. Each hook records into a var for deterministic assertions.
var "hk_set"   { value = false }
var "hk_reset" { value = false }
var "hk_init"  { value = false }
var "hk_act"   { value = false }
var "hk_deact" { value = false }
condition "flipflop" "hooked" {
    set_on        = get(var.hk_set)
    reset_on      = get(var.hk_reset)
    on_init       = set(var.hk_init, true)
    on_activate   = set(var.hk_act, true)
    on_deactivate = set(var.hk_deact, true)
}
