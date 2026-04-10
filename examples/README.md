# Vinculum Examples

This directory contains complete, working Vinculum configurations that
demonstrate how to combine features to solve real problems. See the top-level
[README.md](../README.md) and [doc/overview.md](../doc/overview.md) for an
introduction to Vinculum, and [doc/](../doc/) for the full reference
documentation.

## Examples

### [dns-zone-updater.vcl](dns-zone-updater.vcl)

A dynamic DNS service that exposes a small HTTP API for updating BIND zone
files in place. Demonstrates:

- [`server "http"`](../doc/server-http.md) with route handlers and
  [basic authentication](../doc/server-auth.md)
- A [`procedure`](../doc/procedure.md) block that encapsulates the update logic
  and authorizes callers based on their authenticated username
- An [`editor "line"`](../doc/editor.md) block that performs idempotent,
  locked, line-by-line edits on a zone file (header timestamp, SOA serial bump,
  A-record replace), using "incidental" edits that don't, on their own, count
  as a real file change — so unrelated header/serial updates are discarded
  when no record actually changed

The configuration is compatible with the Unifi Network Controller's Dynamic DNS
feature; see the comments at the top of the file for the controller-side
configuration.

Required environment variables: `ZONES_DIR` (path to the directory containing
BIND zone files) and one `PASS_<ZONE>_<HOST>` variable per credential, as
referenced from the `auth "basic"` block in the example.
