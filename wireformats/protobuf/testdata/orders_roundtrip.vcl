# Integration fixture: round-trips a VCL object through the protobuf wire format
# and asserts every field survives. The descriptor_set is compiled from
# orders.proto by protoc at test time and resolved against --file-path.

wire_format "protobuf" "orders" {
  descriptor_set = "orders.binpb"
  message        = "acme.orders.v1.Order"
}

const {
  original = {
    id      = 42
    name    = "widget"
    active  = true
    amount  = 19.99
    status  = "ACTIVE"
    tags    = ["a", "b", "c"]
    attrs   = { color = "red", size = "L" }
    created = "2026-07-21T12:00:00Z"
    note    = "hello"
    blob    = "raw-bytes"
  }
}

# The round-trip (VCL object -> protobuf binary -> VCL object) is performed
# inside each assert. const cannot reference wire_format.*, but assert can.
assert "id_roundtrips"     { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).id == 42 }
assert "name_roundtrips"   { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).name == "widget" }
assert "active_roundtrips" { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).active == true }
assert "amount_roundtrips" { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).amount == 19.99 }
assert "status_roundtrips" { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).status == "ACTIVE" }
assert "tags_roundtrips"   { condition = length(wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).tags) == 3 }
assert "attrs_roundtrips"  { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).attrs.color == "red" }
assert "note_roundtrips"   { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).note == "hello" }
assert "blob_roundtrips"   { condition = tostring(wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).blob) == "raw-bytes" }
assert "created_present"   { condition = wire::deserialize(wire_format.orders, wire::serialize(wire_format.orders, original)).created != null }
assert "encoded_nonempty"  { condition = length(wire::serialize(wire_format.orders, original)) > 0 }
