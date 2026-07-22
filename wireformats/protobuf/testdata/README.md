# protobuf wire format testdata

`orders.proto` is the schema used by the integration test
(`integration_test.go`, build tag `integration`). The test compiles it to a
`FileDescriptorSet` at run time with a real `protoc`:

```sh
protoc --include_imports --descriptor_set_out=orders.binpb -I . orders.proto
```

so no compiled `.binpb` is committed. The integration test skips when `protoc`
is not installed. The `.vcl` files drive the config pipeline over that schema:

- `orders_roundtrip.vcl` — round-trips a value through `wire::serialize` /
  `wire::deserialize` and asserts every field survives.
- `orders_unknown_field.vcl` — asserts that serializing an object with a field
  not in the message is rejected.

The unit tests (default build) do not use these files; they build descriptor
sets programmatically in `fixtures_test.go` and need no `protoc`.
