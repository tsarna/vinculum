var "x" { value = 0 }

condition "threshold" "backwards" {
    input     = get(var.x)
    on_above  = 5.0
    off_below = 10.0
}
