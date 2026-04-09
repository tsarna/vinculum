procedure "http_status" {
    spec {
        params {
            code = required
        }
    }

    switch "code" {
        case "200" {
            return = "OK"
        }
        case "404" {
            return = "Not Found"
        }
        case "500" {
            return = "Internal Server Error"
        }
        default {
            return = "Unknown"
        }
    }
}

procedure "switch_no_default" {
    spec {
        params {
            x = required
        }
    }

    result = "none"

    switch "x" {
        case "1" {
            result = "one"
        }
        case "2" {
            result = "two"
        }
    }

    return = result
}

procedure "switch_with_assignment" {
    spec {
        params {
            color = required
        }
    }

    hex = ""

    switch "color" {
        case "\"red\"" {
            hex = "#ff0000"
        }
        case "\"green\"" {
            hex = "#00ff00"
        }
        case "\"blue\"" {
            hex = "#0000ff"
        }
        default {
            hex = "#000000"
        }
    }

    return = hex
}

const {
    s200  = http_status(200)
    s404  = http_status(404)
    s500  = http_status(500)
    s999  = http_status(999)
    nd1   = switch_no_default(1)
    nd2   = switch_no_default(2)
    nd3   = switch_no_default(3)
    red   = switch_with_assignment("red")
    blue  = switch_with_assignment("blue")
    other = switch_with_assignment("purple")
}

assert "s200"  { condition = (s200 == "OK") }
assert "s404"  { condition = (s404 == "Not Found") }
assert "s500"  { condition = (s500 == "Internal Server Error") }
assert "s999"  { condition = (s999 == "Unknown") }
assert "nd1"   { condition = (nd1 == "one") }
assert "nd2"   { condition = (nd2 == "two") }
assert "nd3"   { condition = (nd3 == "none") }
assert "red"   { condition = (red == "#ff0000") }
assert "blue"  { condition = (blue == "#0000ff") }
assert "other" { condition = (other == "#000000") }
