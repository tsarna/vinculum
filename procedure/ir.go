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
