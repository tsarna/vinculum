var "x" { value = 0 }

condition "threshold" "mixed" {
    input     = get(var.x)
    on_above  = 10.0
    off_below = 5.0
    on_below  = 1.0
}
