# Deprecated Features

Features listed here still work but are slated for removal. Each entry gives the date
it was deprecated, what to use instead, and when it is expected to be removed. Loading a
configuration that uses a deprecated feature emits a warning (printed by `vinculum
check` and `vinculum serve`).

| Feature | Deprecated | Replacement | Planned removal |
| --- | --- | --- | --- |
| The [`procedure`](procedure.md) block | 2026-07-03 | [functy (`.cty`) files](functy.md) | a future major release |
| Quoted-string `var` `type` (`type = "number"`) | 2026-07-03 | Unquoted type spec (`type = number`) — see [`var`](config.md#var) and [functy types](functy.md#types) | a future release |

Dates are the release in which the deprecation warning was introduced; see
[CHANGELOG.md](../CHANGELOG.md) for the corresponding version.
