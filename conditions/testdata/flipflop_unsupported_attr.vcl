# Error: timeout is not a flipflop attribute (gohcl rejects unknown args).
var "s" { value = false }
condition "flipflop" "bad" {
    set_on  = get(var.s)
    timeout = "1m"
}
