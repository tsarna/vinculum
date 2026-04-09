package procedure

import "github.com/hashicorp/hcl/v2"

// Statement is the interface implemented by all IR nodes.
type Statement interface {
	stmtRange() hcl.Range
}

// Assignment represents a variable assignment: name = expr
type Assignment struct {
	Name     string
	Expr     hcl.Expression
	SrcRange hcl.Range
}

func (a *Assignment) stmtRange() hcl.Range { return a.SrcRange }

// Return represents a return statement: return = expr
type Return struct {
	Expr     hcl.Expression
	SrcRange hcl.Range
}

func (r *Return) stmtRange() hcl.Range { return r.SrcRange }

// IfChain represents an if/elif/else conditional chain.
type IfChain struct {
	Branches []CondBranch // if + elif(s)
	Else     []Statement  // else body, nil if no else clause
	SrcRange hcl.Range
}

func (c *IfChain) stmtRange() hcl.Range { return c.SrcRange }

// CondBranch is a single branch (if or elif) with a condition and body.
type CondBranch struct {
	Condition hcl.Expression
	Body      []Statement
	SrcRange  hcl.Range
}

// While represents a while loop: while "condition" { ... }
type While struct {
	Condition hcl.Expression
	Body      []Statement
	SrcRange  hcl.Range
}

func (w *While) stmtRange() hcl.Range { return w.SrcRange }

// Break represents a break statement: break = bool_expr
type Break struct {
	Condition hcl.Expression
	SrcRange  hcl.Range
}

func (b *Break) stmtRange() hcl.Range { return b.SrcRange }

// Continue represents a continue statement: continue = bool_expr
type Continue struct {
	Condition hcl.Expression
	SrcRange  hcl.Range
}

func (c *Continue) stmtRange() hcl.Range { return c.SrcRange }

// Range represents a range loop: range "item" "collection_expr" { ... }
type Range struct {
	ItemName   string         // iteration variable name
	Collection hcl.Expression // parsed from label
	Body       []Statement
	SrcRange   hcl.Range
}

func (r *Range) stmtRange() hcl.Range { return r.SrcRange }
