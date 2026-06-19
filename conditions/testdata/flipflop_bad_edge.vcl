# Error: "high" is not a valid set_edge (level modes are gate-only).
var "s" { value = false }
condition "flipflop" "bad" {
    set_on   = get(var.s)
    set_edge = "high"
}
