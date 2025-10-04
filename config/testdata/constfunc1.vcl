# this is half of the constfunc.vcl file, to verify that dependency sorting
# works correctly when multiple files are combined

assert "three" {
    condition = (three == 3)
}

const {
    unit_circle_area = circle_area(1.0)
    three = 3
}

assert "pi" {
    condition = (pi == 3.141529)
}

// function uses a constant and a function not defined yet
function "circle_area" {
    params = [r]
    result = pi * square(r)
}