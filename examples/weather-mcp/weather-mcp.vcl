# Weather MCP server
# =================
#
# Exposes live weather data to an MCP client (Claude Desktop, Claude Code, etc.)
# as tools, resources, and a prompt. It wraps the free, no-API-key
# Open-Meteo service (https://open-meteo.com), so it runs with zero
# configuration — no environment variables, no credentials.
#
# Run it:
#   vinculum serve examples/weather-mcp.vcl
#
# Connect Claude Code / Claude Desktop by adding to your MCP config:
#   {"mcpServers": {"weather": {"url": "http://localhost:9000/mcp"}}}
#
# Then ask: "What's the weather in Reykjavik?" or "Pack for 3 days in Oslo."
#
# Optional: export OpenTelemetry traces and metrics by pointing at a collector:
#   OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 vinculum serve examples/weather-mcp.vcl
#
# Demonstrates:
#   - server "mcp" with tools, a templated resource, and a prompt, mounted under
#     a server "http" block (so each MCP call shows up in the HTTP request log)
#   - client "http" wrapping a third-party JSON API (Open-Meteo)
#   - function/jq blocks factoring the geocode→forecast flow out of actions,
#     so each handler resolves the place once and calls the network once
#   - cond() for lazy branching — HCL's own `a ? b : c` eagerly evaluates BOTH
#     branches, which would make a wasted network call on the not-found path;
#     cond() evaluates only the selected branch
#   - returning an empty list as a "not found" sentinel: user functions reject
#     null arguments, so geocode() returns a (zero-or-one) list rather than null
#   - mcp::error() for tool-level failures vs. a plain string for a resource
#   - an optional, env-toggled client "otlp" that the HTTP and MCP servers
#     auto-wire to for traces and metrics

# ── HTTP clients ──────────────────────────────────────────────────────────────

# Name → latitude/longitude lookup. The trailing slash on base_url matters:
# relative call paths ("search") resolve against it to keep the "/v1/" prefix.
client "http" "geocoding" {
    base_url = "https://geocoding-api.open-meteo.com/v1/"
    timeout  = "10s"
}

# Coordinates → forecast.
client "http" "openmeteo" {
    base_url = "https://api.open-meteo.com/v1/"
    timeout  = "10s"
}

# ── Telemetry (optional) ──────────────────────────────────────────────────────
#
# OpenTelemetry export, off by default. Set OTEL_EXPORTER_OTLP_ENDPOINT to an
# OTLP/HTTP collector to enable it; leave it unset and this client is `disabled`,
# so the instrumentation below is a no-op and the example stays zero-config.
# (env.* only contains variables that are actually set, so the unset case is
# read with try(env.X, "") to avoid an "unsupported attribute" error.)
#
# Because this is the only client "otlp" and it sets default_metrics, the HTTP
# server and the mounted MCP server auto-wire to it for both traces and metrics
# — no explicit tracing=/metrics= attributes needed. Each MCP call then produces
# an HTTP server span, a child mcp.server span, and the
# mcp.server.operation.duration metric; the outbound Open-Meteo calls nest as
# client spans under it.

client "otlp" "telemetry" {
    disabled        = try(env.OTEL_EXPORTER_OTLP_ENDPOINT, "") == ""
    endpoint        = try(env.OTEL_EXPORTER_OTLP_ENDPOINT, "")
    service_name    = "weather-mcp"
    default_metrics = true
}

# ── WMO weather-code descriptions ─────────────────────────────────────────────
# Open-Meteo reports conditions as numeric WMO codes; this maps the common
# ones to text. See https://open-meteo.com/en/docs for the full table.

const {
    weather_codes = {
        "0"  = "Clear sky"
        "1"  = "Mainly clear"
        "2"  = "Partly cloudy"
        "3"  = "Overcast"
        "45" = "Fog"
        "48" = "Depositing rime fog"
        "51" = "Light drizzle"
        "53" = "Moderate drizzle"
        "55" = "Dense drizzle"
        "61" = "Slight rain"
        "63" = "Moderate rain"
        "65" = "Heavy rain"
        "71" = "Slight snow"
        "73" = "Moderate snow"
        "75" = "Heavy snow"
        "80" = "Slight rain showers"
        "81" = "Moderate rain showers"
        "82" = "Violent rain showers"
        "95" = "Thunderstorm"
        "96" = "Thunderstorm with slight hail"
        "99" = "Thunderstorm with heavy hail"
    }
}

# ── Helper functions ──────────────────────────────────────────────────────────

# Resolve a place name to its geocoding matches (zero or one, since count=1).
# Returns a list, never null: user functions reject null arguments, so an empty
# list — not null — is the "not found" sentinel that can be passed onward.
# try() returns the first sub-expression that evaluates without error, so a
# response with no "results" key falls through to [] instead of erroring.
function "geocode" {
    params = [ctx, place]
    result = try(
        http::must(http::get(ctx, client.geocoding, "search", {
            as    = "json"
            query = { name = place, count = 1 }
        })).body.results,
        []
    )
}

# Render a WMO weather code as text.
function "weather_desc" {
    params = [code]
    result = lookup(weather_codes, tostring(code), "Weather code ${code}")
}

# Current conditions for an already-resolved location object.
function "current_conditions" {
    params = [ctx, loc]
    result = http::must(http::get(ctx, client.openmeteo, "forecast", {
        as    = "json"
        query = {
            latitude  = loc.latitude
            longitude = loc.longitude
            current   = "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m"
            timezone  = "auto"
        }
    })).body.current
}

# Daily forecast for an already-resolved location object.
function "daily_forecast" {
    params = [ctx, loc, days]
    result = http::must(http::get(ctx, client.openmeteo, "forecast", {
        as    = "json"
        query = {
            latitude      = loc.latitude
            longitude     = loc.longitude
            daily         = "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max"
            forecast_days = days
            timezone      = "auto"
        }
    })).body.daily
}

# Open-Meteo returns daily values as parallel arrays; zip them into one
# object per day so they're easy to format.
jq "forecast_rows" {
    query = <<-EOT
        [ range(0; .time | length) as $i | {
            date:   .time[$i],
            code:   .weather_code[$i],
            high:   .temperature_2m_max[$i],
            low:    .temperature_2m_min[$i],
            precip: .precipitation_probability_max[$i]
        } ]
    EOT
}

# Format current conditions as human-readable text.
function "format_current" {
    params = [loc, cur]
    result = join("\n", [
        "${loc.name}, ${loc.country} (${loc.latitude}, ${loc.longitude})",
        "Conditions:  ${weather_desc(cur.weather_code)}",
        "Temperature: ${cur.temperature_2m}°C (feels like ${cur.apparent_temperature}°C)",
        "Humidity:    ${cur.relative_humidity_2m}%",
        "Wind:        ${cur.wind_speed_10m} km/h",
        "As of ${cur.time} (${loc.timezone})",
    ])
}

# Format a daily forecast as human-readable text.
function "format_forecast" {
    params = [loc, daily]
    result = "Forecast for ${loc.name}, ${loc.country}:\n${join("\n", [
        for d in forecast_rows(daily):
        "  ${d.date}: ${weather_desc(d.code)}, ${d.low}–${d.high}°C, precip ${coalesce(d.precip, 0)}%"
    ])}"
}

# Each handler resolves the place once (in its action, below) and passes the
# matches list in as `matches`, so geocode() runs exactly once. cond() then
# evaluates only the taken branch — the not-found branch never reaches the
# network, and matches[0] is only dereferenced when a match exists. A tool
# reports failure as an MCP error; a resource (which can't return one) reports
# it as plain text.

function "current_weather_report" {
    params = [ctx, place, matches]
    result = cond(
        length(matches) == 0, mcp::error("No location found matching '${place}'. Try a more specific name."),
        format_current(matches[0], current_conditions(ctx, matches[0]))
    )
}

function "current_weather_text" {
    params = [ctx, place, matches]
    result = cond(
        length(matches) == 0, "No location found matching '${place}'.",
        format_current(matches[0], current_conditions(ctx, matches[0]))
    )
}

function "forecast_report" {
    params = [ctx, place, days, matches]
    result = cond(
        length(matches) == 0, mcp::error("No location found matching '${place}'. Try a more specific name."),
        format_forecast(matches[0], daily_forecast(ctx, matches[0], days))
    )
}

# ── MCP server ────────────────────────────────────────────────────────────────
#
# This MCP server has no `listen` of its own — it is mounted under the
# `server "http"` block below, at /mcp. Mounting (rather than giving the MCP
# block its own `listen`) means the HTTP server's request log records every
# inbound MCP call (method, route, status, duration), which a standalone MCP
# server does not emit. It also lets you serve other HTTP routes on the same
# port.

server "mcp" "weather" {
    server_name    = "Weather"
    server_version = "1.0.0"

    # Current weather for a place, addressable as a resource URI.
    # The {place} placeholder arrives as ctx.args.place.
    resource "weather://current/{place}" {
        name        = "Current weather"
        description = "Current conditions for a named place, e.g. weather://current/Tokyo"
        mime_type   = "text/plain"

        action = current_weather_text(ctx, ctx.args.place, geocode(ctx, ctx.args.place))
    }

    tool "current_weather" {
        description = "Get the current weather conditions for a city or place name."

        param "place" {
            type        = "string"
            description = "City or place name, e.g. 'Berlin' or 'Austin, Texas'"
            required    = true
        }

        action = current_weather_report(ctx, ctx.args.place, geocode(ctx, ctx.args.place))
    }

    tool "forecast" {
        description = "Get a multi-day daily forecast (high/low, conditions, precipitation chance)."

        param "place" {
            type        = "string"
            description = "City or place name"
            required    = true
        }
        param "days" {
            type        = "number"
            description = "Number of days to forecast (1–7)"
            default     = 5
        }

        action = forecast_report(ctx, ctx.args.place, ctx.args.days, geocode(ctx, ctx.args.place))
    }

    prompt "trip_packing" {
        description = "Draft packing advice for a trip, grounded in the forecast."

        param "place" {
            type        = "string"
            description = "Destination city or place name"
            required    = true
        }
        param "days" {
            type        = "number"
            description = "Length of the trip in days"
            default     = 3
        }

        action = mcp::user_message(
            "I'm traveling to ${ctx.args.place} for the next ${ctx.args.days} days. Use the `forecast` tool to check the weather there, then tell me what clothing and gear to pack and call out anything weather-dependent I should plan around."
        )
    }
}

# ── HTTP server ───────────────────────────────────────────────────────────────
#
# Hosts the MCP server at /mcp. Each request is logged by the HTTP server, so
# `vinculum serve` prints a line per MCP call. The MCP endpoint is then reachable
# at http://localhost:9000/mcp.
#
# The route has no trailing slash: Streamable HTTP uses a single endpoint, and an
# exact "/mcp" match is what clients expect. A trailing-slash "/mcp/" would be a
# subtree match, so a client connecting to ".../mcp" would be redirected (307)
# and may fail.

server "http" "main" {
    listen = ":9000"

    handle "/mcp" {
        handler = server.weather
    }
}
