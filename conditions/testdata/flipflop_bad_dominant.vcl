# Error: dominant must be "set" or "reset".
var "s" { value = false }
var "r" { value = false }
condition "flipflop" "bad" {
    set_on   = get(var.s)
    reset_on = get(var.r)
    dominant = "neither"
}
