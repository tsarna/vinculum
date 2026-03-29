var "non_null_number" {
    type     = "number"
    nullable = false
    value    = 0
}

var "non_null_any" {
    nullable = false
    value    = "must have a value"
}

assert "non_null_number_initial" {
    condition = get(var.non_null_number) == 0
}

assert "non_null_set_works" {
    condition = set(var.non_null_number, 99) == 99
}

assert "non_null_any_initial" {
    condition = get(var.non_null_any) == "must have a value"
}
