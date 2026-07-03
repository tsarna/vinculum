# A VCL const referencing a functy const (doubled, declared in consts.cty),
# proving the two surfaces share one attribute-level dependency sort.
const {
  vcl_base = 21
  from_functy = doubled + 1
}
