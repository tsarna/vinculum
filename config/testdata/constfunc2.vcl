# This is the other half of the constfunc.vcl file, to verify that dependency sorting works correctly when multiple files are combined

assert "unit_circle_area" {
    condition = (unit_circle_area == pi)
}

// called by a function defined earlier
function "square" {
    params = [x]
    result = x * x
}

const {
    three_circles_area = n_circles_area(three)
    pi = 3.141529
}

// depends on a constant defined by a function
function "n_circles_area" {
    params = [n]
    result = n * unit_circle_area
}

assert "three_circles_area" {
    condition = (three_circles_area == pi * 3)
}