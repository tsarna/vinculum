# Error: set_from requires gate_on.
var "data" { value = false }
condition "flipflop" "bad" {
    set_from = get(var.data)
}
