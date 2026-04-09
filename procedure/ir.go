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
