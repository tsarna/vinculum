var "typed_number" {
    type  = "number"
    value = 0
}

var "typed_string" {
    type  = "string"
    value = "hello"
}

var "typed_no_value" {
    type = "number"
}

assert "typed_number_initial" {
    condition = get(var.typed_number) == 0
}

assert "typed_set_number" {
    condition = set(var.typed_number, 42) == 42
}

assert "typed_string_initial" {
    condition = get(var.typed_string) == "hello"
}

assert "typed_no_value_is_null" {
    condition = get(var.typed_no_value) == null
}
