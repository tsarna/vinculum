# Integration fixture: serializing an object with a field not in the message
# must fail (protobuf is a typed schema; a stray key is almost always a bug).

wire_format "protobuf" "orders" {
  descriptor_set = "orders.binpb"
  message        = "acme.orders.v1.Order"
}

assert "unknown_field_rejected" {
  condition = length(wire::serialize(wire_format.orders, { id = 1, nope = "x" })) > 0
}
