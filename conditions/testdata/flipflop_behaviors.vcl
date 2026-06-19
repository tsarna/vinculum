# Behavioral fixtures for condition "flipflop". Every wire is driven by a
# `var` (a Settable + Watchable boolean) so tests can pulse edges directly.

# --- T flip-flop ---
var "t_btn" { value = false }
condition "flipflop" "lamp" {
    toggle_on = get(var.t_btn) # default toggle_edge = "rising"
}

# --- SR flip-flop (separate sources) ---
var "sr_set" { value = false }
var "sr_reset" { value = false }
condition "flipflop" "fault" {
    set_on   = get(var.sr_set)
    reset_on = get(var.sr_reset)
    # dominant = "reset" (default)
}

# --- SR dominance on a shared source (both wires fire in one notification) ---
var "pulse" { value = false }
condition "flipflop" "sr_reset_dom" {
    set_on   = get(var.pulse)
    reset_on = get(var.pulse)
    # default dominant = "reset"
}
condition "flipflop" "sr_set_dom" {
    set_on   = get(var.pulse)
    reset_on = get(var.pulse)
    dominant = "set"
}

# --- JK flip-flop ---
var "jk_set" { value = false }
var "jk_reset" { value = false }
var "jk_tog" { value = false }
condition "flipflop" "mode" {
    set_on    = get(var.jk_set)
    reset_on  = get(var.jk_reset)
    toggle_on = get(var.jk_tog)
}

# --- D flip-flop (edge-triggered sample) ---
var "d_data" { value = false }
var "d_clk" { value = false }
condition "flipflop" "captured" {
    set_from  = get(var.d_data)
    gate_on   = get(var.d_clk)
    gate_edge = "rising" # default
}

# --- D latch (level-sensitive) ---
var "l_data" { value = false }
var "l_en" { value = false }
condition "flipflop" "tracking" {
    set_from  = get(var.l_data)
    gate_on   = get(var.l_en)
    gate_edge = "high"
}

# --- Gated SR (set/reset effective only while enabled) ---
var "gs_set" { value = false }
var "gs_reset" { value = false }
var "gs_en" { value = false }
condition "flipflop" "controlled" {
    set_on    = get(var.gs_set)
    reset_on  = get(var.gs_reset)
    gate_on   = get(var.gs_en)
    gate_edge = "high"
}
