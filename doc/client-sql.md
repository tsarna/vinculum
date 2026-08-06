# SQL Clients

SQL client blocks let a Vinculum config run SQL statements against a relational
database. Three dialects are supported: `client "postgres"`, `client "mysql"`,
and `client "sqlite"`. All dialects share the same query sub-blocks, parameter
syntax, and result shapes described here, differing only in their connection
settings.

SQL clients have no SQL-specific verbs. They participate in the existing
polymorphic [`get()`](functions.md) and [`call()`](functions.md) functions:

- **`get(ctx, target, ...)`** returns exactly one row as an object keyed by
  column name, and errors if zero or more than one row is returned.
- **`call(ctx, target, ...)`** returns a [result object](#result-object)
  suitable for any number of rows and for modifying statements. SQL execution
  failures are reported in `result.error` — `call()` never raises a config or
  evaluation error for a failed statement.

---

## `client "sqlite" "<name>"`

Requires a cgo-enabled build.[^cgo]

```hcl
client "sqlite" "state" {
    # File path; ":memory:" (the default) for an in-memory database.
    path = "/var/lib/vinculum/state.db"

    # Open mode: "rw" (default, creates if missing), "ro" (read-only, errors
    # if missing), or "rwc".
    mode = "rw"

    # Shared cache across pool connections (ignored for :memory:, which is
    # always opened shared so the pool sees one database).
    shared_cache = false

    # PRAGMAs applied on each new pooled connection.
    pragmas = {
        journal_mode = "WAL"
        synchronous  = "NORMAL"
        foreign_keys = "ON"
        busy_timeout = 5000          # milliseconds
    }

    # Pool tuning (see Pooling below).
    max_open_conns     = 4
    max_idle_conns     = 4
    conn_max_lifetime  = "0s"        # 0s / unset = unlimited
    conn_max_idle_time = "0s"

    # Default per-statement timeout (unset = none; rely on the ctx deadline).
    statement_timeout  = "5s"

    # Named queries (zero or more) — see below.
    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id, name FROM users WHERE id = :id"
    }
}
```

<!-- vinculum:begin block-attrs client sqlite level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `conn_max_idle_time` | string (duration) |  |  | How long an idle connection is kept before it is closed. |
| `conn_max_lifetime` | string (duration) |  |  | How long a connection may be reused before it is retired. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `max_idle_conns` | number |  | `4` | Idle connections kept in the pool. |
| `max_open_conns` | number |  | `4` | Maximum simultaneous connections to the database. |
| `mode` | string |  |  | How to open the database. |
| `path` | string |  |  | Path to the database file. |
| `pragmas` | expression |  |  | PRAGMA statements applied to each new connection. |
| `shared_cache` | bool |  |  | Share one page cache across connections. |
| `statement_timeout` | string (duration) |  |  | Deadline applied to every query on this client. |

**`conn_max_idle_time`**

Idle connections are kept indefinitely when omitted.

**`conn_max_lifetime`**

Connections are reused indefinitely when omitted.

**`disabled`**

The block is parsed and validated, but nothing is created from it.

**`mode`**

`rw` requires it to exist, `rwc` creates it if missing, `ro` opens it read-only.

One of: `rw`, `ro`, `rwc`.

**`path`**

Use `:memory:` for a database that lives only as long as the process.

**`pragmas`**

A map of pragma name to value, for example `{ journal_mode = "WAL" }`.

**`statement_timeout`**

A `query` block can override it. When the inbound `ctx` carries a deadline of its own, the more restrictive of the two wins.

### Blocks

- `query "<name>"` (0..n) — A named, callable SQL statement.

<!-- vinculum:end block-attrs client sqlite -->

[^cgo]: SQLite is compiled in only on cgo-enabled builds (the published
    `vinculum` full image and `vinculum-build` image). On a minimal
    (`CGO_ENABLED=0`) build, using `client "sqlite"` fails at config load with
    *"SQLite not compiled into this build"*.

---

## `client "postgres" "<name>"`

Pure Go (the `pgx` driver needs no cgo), so Postgres is available in both the
full and minimal images.

```hcl
client "postgres" "appdb" {
    # Connection — either a full DSN, or the discrete fields below. The DSN
    # wins if both are given. Both the URL form ("postgres://...") and the
    # libpq keyword/value form ("host=... dbname=...") are accepted.
    dsn = "postgres://${env.PG_USER}:${env.PG_PASS}@db.internal:5432/app?sslmode=require"

    # ── OR discrete fields (user and database are required when dsn is unset) ──
    host     = "db.internal"          # default "localhost"
    port     = 5432                   # default 5432
    user     = env.PG_USER
    password = env.PG_PASS
    database = "appdb"
    sslmode  = "require"              # disable | require | verify-ca | verify-full

    # Default schema search path, applied per connection.
    search_path = "myschema, public"

    # TLS (mTLS / custom CA). Consulted with sslmode = verify-ca/verify-full;
    # mapped to libpq sslrootcert/sslcert/sslkey. Relative paths resolve against
    # the config directory.
    tls {
        ca_cert = "/etc/ssl/myca.pem"
        cert    = "/etc/ssl/client.pem"
        key     = "/etc/ssl/client.key"
        # insecure_skip_verify has no libpq equivalent; for encryption without
        # verification use sslmode = "require". Combining it with a verify-*
        # sslmode is a config error.
    }

    # Pool tuning (see Pooling below).
    max_open_conns     = 25
    max_idle_conns     = 5
    conn_max_lifetime  = "30m"
    conn_max_idle_time = "5m"

    statement_timeout  = "10s"

    # Named queries (zero or more) — see below.
    query "user_by_id" {
        cardinality = "one"
        sql         = "SELECT id, name, email FROM users WHERE id = :id"
    }
}
```

Postgres has no `last_insert_id`; that field is always `null`. To get a
generated key, use `RETURNING` in the statement and read it from `.row`:

```hcl
result = call(ctx, client.appdb,
    "INSERT INTO users (email) VALUES (?) RETURNING id", email)
new_id = result.row.id
```

A `RETURNING` clause makes the statement produce rows, so a named query using it
should declare `cardinality = "one"` (or `"many"`) rather than `"exec"`.

<!-- vinculum:begin block-attrs client postgres level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `conn_max_idle_time` | string (duration) |  |  | How long an idle connection is kept before it is closed. |
| `conn_max_lifetime` | string (duration) |  |  | How long a connection may be reused before it is retired. |
| `database` | string |  |  | Database to connect to. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `dsn` | string |  |  | Full data source name, instead of the individual settings. |
| `host` | string |  |  | Server hostname. |
| `max_idle_conns` | number |  | `5` | Idle connections kept in the pool. |
| `max_open_conns` | number |  | `25` | Maximum simultaneous connections to the database. |
| `password` | string |  |  | Password to connect with. |
| `port` | number |  |  | Server port. |
| `search_path` | string |  |  | Schema search path for the session. |
| `sslmode` | string |  |  | How strictly to verify the server's TLS certificate. |
| `statement_timeout` | string (duration) |  |  | Deadline applied to every query on this client. |
| `user` | string |  |  | User to connect as. |

- Specify at most one of dsn or host.

**`conn_max_idle_time`**

Idle connections are kept indefinitely when omitted.

**`conn_max_lifetime`**

Connections are reused indefinitely when omitted.

**`database`**

Required unless `dsn` is set.

**`disabled`**

The block is parsed and validated, but nothing is created from it.

**`dsn`**

When set, the host/port/user/password/database attributes are ignored.

**`host`**

Defaults to `localhost`.

**`password`**

Supply it from the environment rather than a literal.

**`port`**

Defaults to `5432`.

**`sslmode`**

`verify-full` also checks the hostname; `require` encrypts without verifying anything.

One of: `disable`, `require`, `verify-ca`, `verify-full`.

**`statement_timeout`**

A `query` block can override it. When the inbound `ctx` carries a deadline of its own, the more restrictive of the two wins.

**`user`**

Required unless `dsn` is set.

### Blocks

- `query "<name>"` (0..n) — A named, callable SQL statement.
- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client postgres -->

---

## `client "mysql" "<name>"`

Pure Go (the `go-sql-driver/mysql` driver needs no cgo), so MySQL is available in
both the full and minimal images.

```hcl
client "mysql" "appdb" {
    # Connection — either a full DSN (the go-sql-driver form, NOT a URL), or the
    # discrete fields below. The DSN wins if both are given.
    dsn = "${env.MYSQL_USER}:${env.MYSQL_PASS}@tcp(db.internal:3306)/app"

    # ── OR discrete fields (user and database are required when dsn is unset) ──
    host     = "db.internal"          # default "localhost"
    port     = 3306                   # default 3306
    user     = env.MYSQL_USER
    password = env.MYSQL_PASS
    database = "appdb"

    # TLS (mTLS / custom CA). Built from the shared tls block and registered with
    # the driver. Relative paths resolve against the config directory.
    tls {
        enabled = true
        ca_cert = "/etc/ssl/myca.pem"
        cert    = "/etc/ssl/client.pem"
        key     = "/etc/ssl/client.key"
        # insecure_skip_verify = true   # encrypt without verifying the server cert
    }

    # Pool tuning (see Pooling below).
    max_open_conns     = 25
    max_idle_conns     = 5
    conn_max_lifetime  = "30m"
    conn_max_idle_time = "5m"

    statement_timeout  = "10s"

    query "user_by_id" {
        cardinality = "one"
        sql         = "SELECT id, name, email FROM users WHERE id = :id"
    }
}
```

`parseTime=true` and `loc=UTC` are forced (so `DATETIME`/`TIMESTAMP` columns map
to `time` values rather than strings) unless you override them in an explicit
`dsn`. Unlike Postgres, MySQL **does** populate `last_insert_id` from an
`AUTO_INCREMENT` column after an `INSERT`, so an `"exec"` query is the idiomatic
way to insert and read the new id:

```hcl
result = call(ctx, client.appdb, "INSERT INTO users (email) VALUES (?)", email)
new_id = result.last_insert_id
```

The `tls` block requires `enabled = true`; an absent or disabled block leaves the
connection unencrypted.

<!-- vinculum:begin block-attrs client mysql level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `conn_max_idle_time` | string (duration) |  |  | How long an idle connection is kept before it is closed. |
| `conn_max_lifetime` | string (duration) |  |  | How long a connection may be reused before it is retired. |
| `database` | string |  |  | Database to connect to. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `dsn` | string |  |  | Full data source name, instead of the individual settings. |
| `host` | string |  |  | Server hostname. |
| `max_idle_conns` | number |  | `5` | Idle connections kept in the pool. |
| `max_open_conns` | number |  | `25` | Maximum simultaneous connections to the database. |
| `password` | string |  |  | Password to connect with. |
| `port` | number |  |  | Server port. |
| `statement_timeout` | string (duration) |  |  | Deadline applied to every query on this client. |
| `user` | string |  |  | User to connect as. |

- Specify at most one of dsn or host.

**`conn_max_idle_time`**

Idle connections are kept indefinitely when omitted.

**`conn_max_lifetime`**

Connections are reused indefinitely when omitted.

**`database`**

Required unless `dsn` is set.

**`disabled`**

The block is parsed and validated, but nothing is created from it.

**`dsn`**

When set, the host/port/user/password/database attributes are ignored.

**`host`**

Defaults to `localhost`.

**`password`**

Supply it from the environment rather than a literal.

**`port`**

Defaults to `3306`.

**`statement_timeout`**

A `query` block can override it. When the inbound `ctx` carries a deadline of its own, the more restrictive of the two wins.

**`user`**

Required unless `dsn` is set.

### Blocks

- `query "<name>"` (0..n) — A named, callable SQL statement.
- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client mysql -->

---

## Named Query Sub-blocks

A `query "name" { ... }` sub-block declares a reusable statement, exposed as
`client.<name>.<query_name>` and callable through `get()` and `call()`. Every
dialect takes the same block; this is `client "postgres"`'s copy of it.

<!-- vinculum:begin block-attrs client postgres query level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `sql` | string | yes |  | The SQL statement to run. |
| `cardinality` | string |  | `many` | Shape of the result. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `on_zero` | string |  | `null` | What a `zero_or_one` query does when nothing matches. |
| `statement_timeout` | string (duration) |  |  | Deadline for this query. |

**`sql`**

Use the dialect's placeholder syntax for parameters rather than interpolating values.

**`cardinality`**

`one` returns a single row and errors otherwise; `zero_or_one` allows none; `many` returns a list; `exec` runs a statement that returns no rows.

One of: `one`, `zero_or_one`, `many`, `exec`.

**`disabled`**

The block is parsed and validated, but nothing is created from it.

**`on_zero`**

One of: `null`, `error`.

**`statement_timeout`**

Overrides the client's `statement_timeout`.

<!-- vinculum:end block-attrs client postgres query -->

### Cardinality

Cardinality declares how many rows a query returns and constrains which verb may
target it:

| Cardinality     | `get()` | `call()` | Useful `call()` field             |
|-----------------|---------|----------|-----------------------------------|
| `"one"`         | yes     | yes      | `.row`                            |
| `"zero_or_one"` | yes     | yes      | `.row` (null when zero)           |
| `"many"`        | no      | yes      | `.rows`                           |
| `"exec"`        | no      | yes      | `.affected`, `.last_insert_id`    |

Calling `get()` on a `"many"` or `"exec"` query is an error.

For a `"zero_or_one"` query, `on_zero` controls what `call()` does when the
query returns no rows: `"null"` (the default) yields `row = null`, while
`"error"` reports it in `result.error`. (`get()` always requires exactly one
row, so it errors on zero regardless of `on_zero`.)

---

## Parameter Syntax

The argument after the target is interpreted by shape (SQL clients only):

- **No params:** `call(ctx, client.state, "DELETE FROM scratch")`.
- **One object → named params.** The SQL must use `:name` placeholders. A name
  may repeat; each occurrence binds the same value.

  ```hcl
  get(ctx, client.state, "SELECT * FROM users WHERE id = :id", { id = 42 })
  ```
- **Otherwise → positional params.** The SQL must use `?` placeholders.

  ```hcl
  get(ctx, client.state, "SELECT * FROM users WHERE id = ? AND active = ?", 42, true)
  ```

Mixing `?` and `:name` in one statement, or supplying the wrong parameter shape,
is an evaluation error raised at the call site (not reported in
`result.error`). A missing named key is also a call-site error.

> **Postgres `::type` casts with named parameters.** A statement with **no
> params or positional (`?`) params** may freely use the `value::type` cast
> syntax. In a **named-param** statement, however, the placeholder parser treats
> `::` as an escaped colon and collapses it to a single `:`, breaking the cast.
> In named-param queries use the SQL-standard `CAST(value AS type)` form instead
> (it also works in every other path and is dialect-portable).

For a named query, the same params value is passed directly as the target's
argument:

```hcl
get(ctx, client.state.by_id, { id = 42 })
```

---

## Result Object

`call()` returns:

```hcl
{
    rows           = [ { id = 1, name = "alice" }, ... ]  # all rows; [] if none
    row            = { id = 1, name = "alice" }           # rows[0], or null
    row_count      = 2                                    # length(rows)
    affected       = 0                                    # rows changed by a write
    last_insert_id = null                                 # last ROWID, or null
    error          = null                                 # see below
}
```

On execution failure the row fields are empty/zero and `error` is populated:

```hcl
{
    rows = [], row = null, row_count = 0, affected = 0, last_insert_id = null,
    error = {
        code     = "2067"        # SQLite extended result code (string)
        message  = "UNIQUE constraint failed: users.email"
        sqlstate = ""            # empty for SQLite
        driver   = "sqlite"
    }
}
```

Because the error rides in the result rather than aborting evaluation, a
handler can inspect `result.error` directly.

### `sql::must(result)`

When you would rather fail the whole action on any database error than handle
it inline, wrap the call in `sql::must()`. If `result.error` is non-null it
raises an evaluation error built from the error fields (driver, code, sqlstate,
message); otherwise it returns the result unchanged, so it composes inline:

```hcl
action = http::response(http_status.OK, {
    id = sql::must(
        call(ctx, client.state, "INSERT INTO users (email) VALUES (?)", email)
    ).last_insert_id,
})
```

`sql::must()` is available in every action context (it works on any result
object that carries an `error` field), so it does not require a SQL client to
be configured.

---

## Type Mapping

Result columns map to cty values primarily by the scanned value's type, using
the column's declared type as a hint:

| cty result                 | SQLite columns               | Postgres columns                                            | MySQL columns                                             |
|----------------------------|------------------------------|-------------------------------------------------------------|-----------------------------------------------------------|
| number                     | `INTEGER`, `REAL`, `NUMERIC` | `smallint`, `integer`, `bigint`, `real`, `double precision` | `tinyint`, `smallint`, `int`, `bigint`, `float`, `double` |
| bool                       | `BOOLEAN`                    | `boolean`                                                   | — (use `tinyint`)                                         |
| string                     | `TEXT`                       | `text`, `varchar`, `char`, `uuid`, `time`, `interval`       | `varchar`, `text`, `char`, `time`                         |
| `bytes` object             | `BLOB`                       | `bytea`                                                     | `blob`, `varbinary`                                       |
| time                       | `DATETIME`, `DATE`           | `timestamp`, `timestamptz`, `date`                          | `datetime`, `timestamp`, `date`                           |
| decoded object/list/scalar | `JSON` (TEXT)                | `json`, `jsonb`                                             | `json`                                                    |
| `null`                     | any NULL                     | any NULL                                                    | any NULL                                                  |

JSON columns are decoded to their underlying value; a value that fails to parse
falls back to the raw string. `numeric`/`decimal` columns are returned by the
Postgres and MySQL drivers as strings (preserving exact precision) rather than
`number`. MySQL `tinyint(1)` columns surface as a `number` (0/1), not `bool`.
Driver-specific types outside this table (Postgres arrays, ranges, enums, etc.)
are returned as strings via the driver's default scan.

Bound parameters use the inverse mapping: `null` binds SQL `NULL`, a `bytes`
value binds a blob/`bytea`, a `time` value binds a timestamp, and
numbers/strings/bools bind directly.

---

## Pooling

SQL clients expose the standard `database/sql` pool knobs: `max_open_conns`,
`max_idle_conns`, `conn_max_lifetime`, and `conn_max_idle_time`. Defaults are
dialect-flavored: SQLite uses `max_open_conns = 4`, `max_idle_conns = 4`;
Postgres and MySQL use `max_open_conns = 25`, `max_idle_conns = 5`. All default
to unlimited connection lifetimes.

When the inbound `ctx` carries a deadline (e.g. an HTTP request timeout), it is
passed through to the database, and the effective per-statement timeout is the
**more restrictive** of that deadline and the configured `statement_timeout`.

---

## Examples

### Enrichment in an HTTP handler

```hcl
client "sqlite" "users" {
    path = "/var/lib/vinculum/users.db"
    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id, name, email FROM users WHERE id = :id"
    }
}

server "http" "api" {
    listen = ":8080"
    handle "GET /users/{id}" {
        action = http::response(http_status.OK, get(ctx, client.users.by_id, {
            id = toint(ctx.request.path.id),
        }))
    }
}
```

### Persistence from a subscription

```hcl
client "sqlite" "events" {
    path = "/var/lib/vinculum/events.db"
    query "insert_event" {
        cardinality = "exec"
        sql         = "INSERT INTO events (topic, payload) VALUES (:topic, :payload)"
    }
}

bus "incoming" {}

subscription "persist" {
    target = bus.incoming
    topics = ["events/#"]
    action = call(ctx, client.events.insert_event, {
        topic   = ctx.topic,
        payload = jsonencode(ctx.msg),
    })
}
```

### Periodic cleanup with inline SQL

```hcl
client "sqlite" "state" {
    path    = "/var/lib/vinculum/state.db"
    pragmas = { journal_mode = "WAL", foreign_keys = "ON" }
}

cron "hourly_cleanup" {
    at "0 * * * *" "purge" {
        action = call(ctx, client.state,
            "DELETE FROM scratch WHERE created_at < datetime('now', '-1 day')")
    }
}
```

---

## Not Yet Supported

Transactions, streaming row iterators, change feeds (LISTEN/NOTIFY, update
hooks), multi-statement scripts, and driver-specific types beyond the common
subset are out of scope for this release.
