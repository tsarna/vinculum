// These are deliberately in a horrible order to verify that const dependency sorting
// works correctly, and that consts and user functions can use each other

assert "three" {
    condition = (three == 3)
}

const {
    three_circles_area = n_circles_area(three)
    pi = 3.141529
    unit_circle_area = circle_area(1.0)
    three = 3
    //four = two * two
}

assert "pi" {
    condition = (pi == 3.141529)
}

// function uses a constant and a function not defined yet
function "circle_area" {
    params = [r]
    result = pi * square(r)
}

assert "unit_circle_area" {
    condition = (unit_circle_area == pi)
}

// called by a function defined earlier
function "square" {
    params = [x]
    result = x * x
}

// depends on a constant defined by a function
function "n_circles_area" {
    params = [n]
    result = n * unit_circle_area
}

assert "three_circles_area" {
    condition = (three_circles_area == pi * 3)
}