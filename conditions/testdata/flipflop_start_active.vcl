# start_active boots the output true with no synthetic on_activate; on_init
# sees new_value = true. Hooks record into vars for deterministic assertions.
var "sa_reset"     { value = false }
var "sa_init_new"  { value = false }
var "sa_activated" { value = false }
condition "flipflop" "armed" {
    reset_on     = get(var.sa_reset)
    start_active = true
    on_init      = set(var.sa_init_new, ctx.new_value)
    on_activate  = set(var.sa_activated, true)
}
