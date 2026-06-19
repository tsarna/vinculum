# Error: gate_on with no driven wire.
var "clk" { value = false }
condition "flipflop" "bad" {
    gate_on = get(var.clk)
}
