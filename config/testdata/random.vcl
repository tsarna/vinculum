const {
  r   = random()
  ri  = randint(3, 7)
  ru  = randuniform(2.0, 5.0)
  rg  = randgauss(10.0, 1.0)
  rc  = randchoice(["x", "y", "z"])
  rs  = randsample([1, 2, 3, 4, 5], 3)
  rsh = randshuffle([10, 20, 30])
}

assert "random_gte_zero" {
  condition = (r >= 0)
}

assert "random_lt_one" {
  condition = (r < 1)
}

assert "randint_lower" {
  condition = (ri >= 3)
}

assert "randint_upper" {
  condition = (ri <= 7)
}

assert "randuniform_lower" {
  condition = (ru >= 2.0)
}

assert "randuniform_upper" {
  condition = (ru <= 5.0)
}

assert "randgauss_is_number" {
  condition = (typeof(rg) == "number")
}

assert "randchoice_valid" {
  condition = contains(["x", "y", "z"], rc)
}

assert "randsample_length" {
  condition = (length(rs) == 3)
}

assert "randshuffle_length" {
  condition = (length(rsh) == 3)
}
