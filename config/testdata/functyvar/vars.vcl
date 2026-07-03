const {
  base = 10
}

# Asserts run in the Process phase; functy var values are set before it, so they
# are visible here — proving a functy var is equivalent to a VCL var block.
assert "counter_init" {
  condition = get(var.counter) == 10
}

assert "label_init" {
  condition = get(var.label) == "start"
}

assert "counter_settable" {
  condition = set(var.counter, 42) == 42
}
