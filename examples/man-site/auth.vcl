# Who may read the reference, and who may check a configuration
# ==============================================================
#
# One file, two postures, chosen by environment rather than by editing:
#
#   Nothing set          a public, anonymous, read-only reference
#   MAN_PASSWORD         every route needs that password
#   MAN_CHECK_PASSWORD   the checker is offered, and the MCP route needs *that*
#                        password — the one variable both switches the checker
#                        on and shuts the door in front of it
#
# Checking builds text a caller sent. That build is fenced (see mcp.vcl and
# doc/functions.md), but a `tls` block in a submitted config still reads the
# files it names, and its error says whether a path exists. So the checker is
# reachable only with a password, and there is no variable that turns it on
# without one.

# A disabled auth block is parsed but inert, and its required attributes are
# *not* validated — which is what lets one variable both supply the credential
# and switch the mechanism on. Without that, an unset password would still have
# to satisfy `credentials`.
auth "basic" "site" {
    disabled    = try(env.MAN_PASSWORD, "") == ""
    realm       = "Vinculum reference"
    credentials = { (try(env.MAN_USER, "docs")) = try(env.MAN_PASSWORD, "") }
}

auth "basic" "checker" {
    disabled    = try(env.MAN_CHECK_PASSWORD, "") == ""
    realm       = "Vinculum checker"
    credentials = { (try(env.MAN_CHECK_USER, "check")) = try(env.MAN_CHECK_PASSWORD, "") }
}

# The policies are written at the routes, in mcp.vcl, rather than folded into
# a const here. A `const` is evaluated before any `auth` block is processed, so
# `auth.site` does not resolve in one — and writing the reference at the route
# is also what lets the dependency sort order the server after the auth blocks
# it names.
#
# Both spellings are unauthenticated when nothing is set, but a route left with
# a policy that is merely switched off logs a warning at startup naming the
# route, while auth.anonymous is the documented way to say "public on purpose".
# This deployment is public on purpose, and the warning should stay meaningful
# for everyone else.
