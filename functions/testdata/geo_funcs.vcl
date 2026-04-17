// End-to-end smoke tests for geo-cty-funcs integration.
// Each assert block verifies one function category.

// --- Point construction and formatting ---

const {
    p_num   = geo_point(37.7749, -122.4194)
    p_dms   = geo_point("37°46'29\"N", "122°25'9\"W")
    p_combo = geo_point("37.7749,-122.4194")
    p_base  = geo_point(10.0, 20.0, {alt = 100.0})
}

assert "geo_point_numbers" {
    condition = (p_num.lat == 37.7749 && p_num.lon == -122.4194)
}

assert "geo_point_combined" {
    condition = (p_combo.lat == 37.7749 && p_combo.lon == -122.4194)
}

assert "geo_point_base_preserves_fields" {
    condition = (p_base.alt == 100.0 && p_base.lat == 10.0)
}

assert "geo_format_decimal" {
    condition = (geo_format(p_num) == "37.7749,-122.4194")
}

assert "geo_format_round_trip" {
    condition = (geo_point(geo_format(p_num)).lat == 37.7749)
}

// --- Solar functions ---

const {
    ref_time  = parsetime("2025-03-20T00:00:00Z")
    sf        = geo_point(37.7749, -122.4194)
    sf_rise   = sunrise(sf, ref_time)
    sf_set    = sunset(sf, ref_time)
    sf_noon   = solar_noon(sf, ref_time)
    sf_mid    = solar_midnight(sf, ref_time)
}

assert "sunrise_after_ref" {
    condition = timeafter(sf_rise, ref_time)
}

assert "sunset_after_ref" {
    condition = timeafter(sf_set, ref_time)
}

assert "solar_noon_after_ref" {
    condition = timeafter(sf_noon, ref_time)
}

assert "solar_midnight_after_ref" {
    condition = timeafter(sf_mid, ref_time)
}

// --- Sun/moon position ---

const {
    equator_noon = parsetime("2025-03-20T12:00:00Z")
    equator      = geo_point(0, 0)
    sun          = sun_position(equator, equator_noon)
    moon         = moon_position(sf, ref_time)
    phase        = moon_phase(ref_time)
}

assert "sun_altitude_positive_at_noon" {
    condition = (sun.altitude > 60)
}

assert "sun_azimuth_in_range" {
    condition = (sun.azimuth >= 0 && sun.azimuth < 360)
}

assert "moon_distance_reasonable" {
    condition = (moon.distance > 356000000 && moon.distance < 406000000)
}

assert "moon_phase_in_range" {
    condition = (phase.fraction >= 0 && phase.fraction <= 1 && phase.phase >= 0 && phase.phase <= 1)
}

// --- Geodesic functions ---

const {
    nyc       = geo_point(40.7128, -74.0060)
    inv       = geo_inverse(sf, nyc)
    dest      = geo_destination(sf, 90.0, 1000.0)
    waypoints = geo_waypoints(sf, nyc, 5)
}

assert "geo_inverse_distance" {
    // SF to NYC ≈ 4,139 km
    condition = (inv.distance > 4000000 && inv.distance < 4300000)
}

assert "geo_inverse_bearing" {
    condition = (inv.bearing > 60 && inv.bearing < 80)
}

assert "geo_destination_preserves_lat" {
    // 1 km east should barely change latitude
    condition = (dest.lat > 37.77 && dest.lat < 37.78)
}

assert "geo_waypoints_count" {
    condition = (length(waypoints) == 5)
}

assert "geo_waypoints_first_near_sf" {
    // First waypoint should be at SF (within float tolerance)
    condition = (abs(waypoints[0].lat - sf.lat) < 0.0001 &&
                 abs(waypoints[0].lon - sf.lon) < 0.0001)
}

assert "geo_waypoints_last_near_nyc" {
    condition = (abs(waypoints[4].lat - nyc.lat) < 0.0001 &&
                 abs(waypoints[4].lon - nyc.lon) < 0.0001)
}

// --- Geometric functions ---

const {
    polygon = [
        geo_point(37.780, -122.420),
        geo_point(37.780, -122.410),
        geo_point(37.770, -122.410),
        geo_point(37.770, -122.420),
    ]
    center  = geo_point(37.775, -122.415)
    outside = geo_point(38.0, -122.0)
}

assert "geo_area_positive" {
    condition = (geo_area(polygon) > 0)
}

assert "geo_contains_inside" {
    condition = geo_contains(polygon, center)
}

assert "geo_contains_outside" {
    condition = !geo_contains(polygon, outside)
}

assert "geo_nearest_on_perimeter" {
    // Nearest point to something east of the polygon should have lon ≈ -122.410
    condition = (geo_nearest(polygon, geo_point(37.775, -122.405)).lon > -122.411 &&
                 geo_nearest(polygon, geo_point(37.775, -122.405)).lon < -122.409)
}

assert "geo_line_intersect_crossing" {
    condition = (length(geo_line_intersect(
        [geo_point(0, 0), geo_point(1, 1)],
        [geo_point(0, 1), geo_point(1, 0)]
    )) == 1)
}

assert "geo_line_intersect_parallel" {
    condition = (length(geo_line_intersect(
        [geo_point(0, 0), geo_point(1, 0)],
        [geo_point(0, 1), geo_point(1, 1)]
    )) == 0)
}
