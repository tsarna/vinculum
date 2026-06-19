# cooldown enforces a quiet period after deactivation before a new activation
# takes effect. Driven through the state machine's clock (swapped for a fake in
# the test).
var "cd_set" { value = false }
var "cd_reset" { value = false }
condition "flipflop" "cooled" {
    set_on   = get(var.cd_set)
    reset_on = get(var.cd_reset)
    cooldown = "5m"
}
