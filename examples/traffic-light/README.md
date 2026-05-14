# Traffic Light Intersection

A simulated four-way traffic light intersection, modeled as a state machine
with fault detection, emergency-vehicle preemption, late-night
flashing mode, and manual override. A web UI shows the intersection live and
lets you trigger faults and overrides; updates are pushed over a WebSocket as
the underlying state changes.

A running instance is available at <https://traffic.thevinculum.org>.

This is the most feature-dense of the included examples. It demonstrates how
the various Vinculum block types compose to model a real control system
without writing any significant code.

## Demonstrates

- [`fsm`](../../doc/fsm.md) — the intersection's normal phase cycle, fault
  states, latenight flashing, and emergency preempt are all expressed as
  transitions on a single state machine. Guards combine FSM storage
  (`last_green`) with conditions and variables to pick the next state.
- [`condition "timer"`](../../doc/condition.md) — latched conditions for
  master / power-outage / manual / conflict / timeout faults and for the
  emergency-preempt and manual-phases overrides, with `on_init`,
  `on_activate`, and `on_deactivate` hooks publishing status to the bus.
- [`trigger "interval"`](../../doc/trigger.md), `trigger "cron"`,
  `trigger "start"`, `trigger "watchdog"` — phase advancement, the 500ms
  blink, midnight/6am late-night mode switching, initial-mode selection on
  startup, and an FSM watchdog that detects a stuck state machine.
- [`var`](../../doc/config.md) — per-direction light state and a global mode
  variable, written by subscriptions and read by FSM guards.
- [`subscription`](../../doc/config.md) — fans out bus messages to the FSM,
  to variables, and to conditions; `switch()` dispatches on a topic field to
  pick which variable or condition to update.
- [`server "http"`](../../doc/server-http.md) +
  [`server "vws"`](../../doc/server-vws.md) — serves the static web UI, a
  REST status endpoint, and mounts a VWS WebSocket so the browser can
  subscribe to live bus updates and publish overrides.
- [`function`](../../doc/functions.md) — a small `allows_traffic()` helper
  reused inside the conflict-detection condition.

## Layout

The configuration is split across small topical files; Vinculum loads every
`.vcl` file in the directory as a single configuration.

| File | Contents |
|---|---|
| [lights.vcl](lights.vcl) | Bus, per-direction light variables, the intersection FSM, and the phase/blink interval triggers |
| [fault.vcl](fault.vcl) | Master fault condition aggregating power-outage, manual, conflict, and timeout sub-conditions, plus the watchdog and fault reset/trigger subscriptions |
| [emergency.vcl](emergency.vcl) | Emergency-vehicle preemption condition and its trigger subscription |
| [late-night.vcl](late-night.vcl) | `mode` variable, cron switches between `NORMAL` and `LATE_NIGHT`, and the initial-mode startup trigger |
| [manual.vcl](manual.vcl) | Manual-phases override condition and its trigger subscription |
| [web.vcl](web.vcl) | HTTP server (static files, `/api/traffic/status` JSON endpoint, mounted VWS) and the VWS WebSocket server |
| [html/](html/) | The single-page web UI (vanilla HTML/CSS/JS) |

## Bus topics

All traffic-related messages live under the `traffic/` prefix:

| Topic | Direction | Purpose |
|---|---|---|
| `traffic/lights/{direction}` | published by FSM | Light state per direction (`RED` / `YELLOW` / `GREEN` / `OFF`) |
| `traffic/state` | published by FSM | Current FSM state name |
| `traffic/mode` | both | Operating mode (`NORMAL` / `LATE_NIGHT`) |
| `traffic/next_phase` | published by phase trigger | Advances the FSM to its next phase |
| `traffic/fault/status/{name}` | published by fault conditions | Current value of each fault |
| `traffic/fault/trigger/manual` | inbound | Set/clear the manual fault |
| `traffic/fault/reset/{name}` | inbound | Clear a latched fault |
| `traffic/emergency/set` | inbound | Set/clear emergency preempt |
| `traffic/emergency/status` | published | Current emergency state |
| `traffic/manual_phases/set` | inbound | Enter/leave manual phase control |
| `traffic/manual_phases/status` | published | Current manual-phases state |
