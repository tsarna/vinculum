package procedure

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Compile compiles an HCL body (the procedure body after spec extraction)
// into an ordered list of IR statements.
func Compile(body hcl.Body, filename string) ([]Statement, hcl.Diagnostics) {
	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unsupported HCL format",
			Detail:   "Procedure bodies must use HCL native syntax",
		}}
	}

	items := sortBodyItems(syntaxBody)
	return compileItems(items, 0)
}

// bodyItem is a union of attribute or block, sorted by byte offset.
type bodyItem struct {
	offset int
	attr   *hclsyntax.Attribute // non-nil for attributes
	block  *hclsyntax.Block     // non-nil for blocks
}

func sortBodyItems(body *hclsyntax.Body) []bodyItem {
	var items []bodyItem
	for _, attr := range body.Attributes {
		items = append(items, bodyItem{offset: attr.SrcRange.Start.Byte, attr: attr})
	}
	for _, block := range body.Blocks {
		items = append(items, bodyItem{offset: block.Range().Start.Byte, block: block})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].offset < items[j].offset
	})
	return items
}

// compileItems compiles a sorted list of body items into statements.
// loopDepth tracks nesting for break/continue validation.
func compileItems(items []bodyItem, loopDepth int) ([]Statement, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var stmts []Statement
	terminated := false // true after a return in this scope

	for i := 0; i < len(items); i++ {
		item := items[i]

		if terminated {
			r := itemRange(item)
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unreachable statement",
				Detail:   "This statement can never execute because a previous statement always exits.",
				Subject:  &r,
			})
			return stmts, diags
		}

		if item.attr != nil {
			stmt, attrDiags := compileAttribute(item.attr, loopDepth)
			diags = diags.Extend(attrDiags)
			if attrDiags.HasErrors() {
				return nil, diags
			}
			stmts = append(stmts, stmt)
			switch stmt.(type) {
			case *Return:
				terminated = true
			case *Break, *Continue:
				if isUnconditionalSignal(item.attr) {
					terminated = true
				}
			}
		} else {
			stmt, consumed, blockDiags := compileBlock(items, i, loopDepth)
			diags = diags.Extend(blockDiags)
			if blockDiags.HasErrors() {
				return nil, diags
			}
			stmts = append(stmts, stmt)
			i += consumed // skip any elif/else blocks consumed by this chain
			if alwaysTerminates(stmt) {
				terminated = true
			}
		}
	}

	return stmts, diags
}

// compileBlock compiles a block item and any following elif/else blocks into
// an IR statement. Returns the statement, the number of additional items
// consumed (for elif/else chaining), and diagnostics.
func compileBlock(items []bodyItem, idx int, loopDepth int) (Statement, int, hcl.Diagnostics) {
	block := items[idx].block

	switch block.Type {
	case "if":
		return compileIfChain(items, idx, loopDepth)
	case "elif":
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unexpected elif",
			Detail:   "elif must immediately follow an if or elif block.",
			Subject:  block.DefRange().Ptr(),
		}}
	case "else":
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unexpected else",
			Detail:   "else must immediately follow an if or elif block.",
			Subject:  block.DefRange().Ptr(),
		}}
	case "range":
		return compileRange(block, loopDepth)
	case "switch":
		return compileSwitch(block, loopDepth)
	case "while":
		return compileWhile(block, loopDepth)
	default:
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown block type",
			Detail:   fmt.Sprintf("Block type %q is not supported in procedures.", block.Type),
			Subject:  block.DefRange().Ptr(),
		}}
	}
}

// compileIfChain compiles an if block and any immediately following elif/else
// blocks into an IfChain IR node.
func compileIfChain(items []bodyItem, idx int, loopDepth int) (Statement, int, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	chain := &IfChain{
		SrcRange: items[idx].block.Range(),
	}

	// Compile the initial "if" branch
	branch, branchDiags := compileCondBranch(items[idx].block, "if", loopDepth)
	diags = diags.Extend(branchDiags)
	if branchDiags.HasErrors() {
		return nil, 0, diags
	}
	chain.Branches = append(chain.Branches, *branch)

	// Consume following elif/else blocks
	consumed := 0
	for next := idx + 1; next < len(items); next++ {
		if items[next].attr != nil {
			break // not a block, chain ends
		}
		block := items[next].block

		switch block.Type {
		case "elif":
			if chain.Else != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "elif after else",
					Detail:   "elif cannot appear after an else block.",
					Subject:  block.DefRange().Ptr(),
				})
				return nil, 0, diags
			}
			branch, branchDiags := compileCondBranch(block, "elif", loopDepth)
			diags = diags.Extend(branchDiags)
			if branchDiags.HasErrors() {
				return nil, 0, diags
			}
			chain.Branches = append(chain.Branches, *branch)
			consumed++

		case "else":
			if chain.Else != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate else",
					Detail:   "Only one else block is allowed per if chain.",
					Subject:  block.DefRange().Ptr(),
				})
				return nil, 0, diags
			}
			if len(block.Labels) > 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid else block",
					Detail:   "else takes no labels.",
					Subject:  block.DefRange().Ptr(),
				})
				return nil, 0, diags
			}
			bodyItems := sortBodyItems(block.Body)
			elseStmts, elseDiags := compileItems(bodyItems, loopDepth)
			diags = diags.Extend(elseDiags)
			if elseDiags.HasErrors() {
				return nil, 0, diags
			}
			chain.Else = elseStmts
			consumed++

		default:
			// Not part of the chain
			goto done
		}
	}
done:

	return chain, consumed, diags
}

// compileCondBranch compiles an if or elif block into a CondBranch.
func compileCondBranch(block *hclsyntax.Block, kind string, loopDepth int) (*CondBranch, hcl.Diagnostics) {
	if len(block.Labels) != 1 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("Invalid %s block", kind),
			Detail:   fmt.Sprintf("%s requires exactly one label: a quoted condition expression.", kind),
			Subject:  block.DefRange().Ptr(),
		}}
	}

	// Parse the label as an HCL expression
	condExpr, diags := hclsyntax.ParseExpression(
		[]byte(block.Labels[0]),
		block.DefRange().Filename,
		block.DefRange().Start,
	)
	if diags.HasErrors() {
		return nil, diags
	}

	bodyItems := sortBodyItems(block.Body)
	bodyStmts, bodyDiags := compileItems(bodyItems, loopDepth)
	diags = diags.Extend(bodyDiags)
	if bodyDiags.HasErrors() {
		return nil, diags
	}

	return &CondBranch{
		Condition: condExpr,
		Body:      bodyStmts,
		SrcRange:  block.Range(),
	}, nil
}

// compileWhile compiles a while block into a While IR node.
func compileWhile(block *hclsyntax.Block, loopDepth int) (Statement, int, hcl.Diagnostics) {
	if len(block.Labels) != 1 {
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid while block",
			Detail:   "while requires exactly one label: a quoted condition expression.",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	condExpr, diags := hclsyntax.ParseExpression(
		[]byte(block.Labels[0]),
		block.DefRange().Filename,
		block.DefRange().Start,
	)
	if diags.HasErrors() {
		return nil, 0, diags
	}

	bodyItems := sortBodyItems(block.Body)
	bodyStmts, bodyDiags := compileItems(bodyItems, loopDepth+1)
	diags = diags.Extend(bodyDiags)
	if bodyDiags.HasErrors() {
		return nil, 0, diags
	}

	return &While{
		Condition: condExpr,
		Body:      bodyStmts,
		SrcRange:  block.Range(),
	}, 0, nil
}

// compileRange compiles a range block into a Range IR node.
func compileRange(block *hclsyntax.Block, loopDepth int) (Statement, int, hcl.Diagnostics) {
	if len(block.Labels) != 2 {
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid range block",
			Detail:   "range requires exactly two labels: an item variable name and a quoted collection expression.",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	itemName := block.Labels[0]

	collExpr, diags := hclsyntax.ParseExpression(
		[]byte(block.Labels[1]),
		block.DefRange().Filename,
		block.DefRange().Start,
	)
	if diags.HasErrors() {
		return nil, 0, diags
	}

	bodyItems := sortBodyItems(block.Body)
	bodyStmts, bodyDiags := compileItems(bodyItems, loopDepth+1)
	diags = diags.Extend(bodyDiags)
	if bodyDiags.HasErrors() {
		return nil, 0, diags
	}

	return &Range{
		ItemName:   itemName,
		Collection: collExpr,
		Body:       bodyStmts,
		SrcRange:   block.Range(),
	}, 0, nil
}

// compileSwitch compiles a switch block into a Switch IR node.
func compileSwitch(block *hclsyntax.Block, loopDepth int) (Statement, int, hcl.Diagnostics) {
	if len(block.Labels) != 1 {
		return nil, 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid switch block",
			Detail:   "switch requires exactly one label: a quoted subject expression.",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	subjExpr, diags := hclsyntax.ParseExpression(
		[]byte(block.Labels[0]),
		block.DefRange().Filename,
		block.DefRange().Start,
	)
	if diags.HasErrors() {
		return nil, 0, diags
	}

	// Validate: no attributes directly in switch body
	if len(block.Body.Attributes) > 0 {
		// Pick the first attribute for the error location
		for _, attr := range block.Body.Attributes {
			return nil, 0, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Assignment in switch body",
				Detail:   "Assignments are not allowed directly inside a switch block. Only case and default blocks are permitted.",
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
	}

	sw := &Switch{
		Subject:  subjExpr,
		SrcRange: block.Range(),
	}

	for _, inner := range block.Body.Blocks {
		switch inner.Type {
		case "case":
			if len(inner.Labels) != 1 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid case block",
					Detail:   "case requires exactly one label: a quoted value expression.",
					Subject:  inner.DefRange().Ptr(),
				})
				return nil, 0, diags
			}

			valExpr, parseDiags := hclsyntax.ParseExpression(
				[]byte(inner.Labels[0]),
				inner.DefRange().Filename,
				inner.DefRange().Start,
			)
			diags = diags.Extend(parseDiags)
			if parseDiags.HasErrors() {
				return nil, 0, diags
			}

			bodyItems := sortBodyItems(inner.Body)
			bodyStmts, bodyDiags := compileItems(bodyItems, loopDepth)
			diags = diags.Extend(bodyDiags)
			if bodyDiags.HasErrors() {
				return nil, 0, diags
			}

			sw.Cases = append(sw.Cases, Case{
				Value:    valExpr,
				Body:     bodyStmts,
				SrcRange: inner.Range(),
			})

		case "default":
			if sw.Default != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate default",
					Detail:   "Only one default block is allowed per switch.",
					Subject:  inner.DefRange().Ptr(),
				})
				return nil, 0, diags
			}
			if len(inner.Labels) > 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid default block",
					Detail:   "default takes no labels.",
					Subject:  inner.DefRange().Ptr(),
				})
				return nil, 0, diags
			}

			bodyItems := sortBodyItems(inner.Body)
			bodyStmts, bodyDiags := compileItems(bodyItems, loopDepth)
			diags = diags.Extend(bodyDiags)
			if bodyDiags.HasErrors() {
				return nil, 0, diags
			}
			sw.Default = bodyStmts

		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block in switch",
				Detail:   fmt.Sprintf("Block type %q is not allowed inside a switch. Only case and default are permitted.", inner.Type),
				Subject:  inner.DefRange().Ptr(),
			})
			return nil, 0, diags
		}
	}

	return sw, 0, diags
}

// alwaysTerminates returns true if a statement unconditionally exits the
// procedure (or the current scope, for unreachable code detection).
func alwaysTerminates(stmt Statement) bool {
	switch s := stmt.(type) {
	case *Return:
		return true
	case *IfChain:
		if s.Else == nil {
			return false
		}
		for _, branch := range s.Branches {
			if !bodyAlwaysTerminates(branch.Body) {
				return false
			}
		}
		return bodyAlwaysTerminates(s.Else)
	case *Switch:
		if s.Default == nil {
			return false
		}
		for _, c := range s.Cases {
			if !bodyAlwaysTerminates(c.Body) {
				return false
			}
		}
		return bodyAlwaysTerminates(s.Default)
	default:
		return false
	}
}

// bodyAlwaysTerminates returns true if a body always exits via return.
func bodyAlwaysTerminates(stmts []Statement) bool {
	for _, stmt := range stmts {
		if alwaysTerminates(stmt) {
			return true
		}
	}
	return false
}

func compileAttribute(attr *hclsyntax.Attribute, loopDepth int) (Statement, hcl.Diagnostics) {
	switch attr.Name {
	case "return":
		return &Return{
			Expr:     attr.Expr,
			SrcRange: attr.SrcRange,
		}, nil

	case "break":
		if loopDepth == 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Break outside loop",
				Detail:   "break can only be used inside a while or range loop.",
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
		return &Break{
			Condition: attr.Expr,
			SrcRange:  attr.SrcRange,
		}, nil

	case "continue":
		if loopDepth == 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Continue outside loop",
				Detail:   "continue can only be used inside a while or range loop.",
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
		return &Continue{
			Condition: attr.Expr,
			SrcRange:  attr.SrcRange,
		}, nil

	default:
		return &Assignment{
			Name:     attr.Name,
			Expr:     attr.Expr,
			SrcRange: attr.SrcRange,
		}, nil
	}
}

// isUnconditionalSignal returns true if the attribute's expression is the
// literal `true`, meaning the break/continue always fires.
func isUnconditionalSignal(attr *hclsyntax.Attribute) bool {
	lit, ok := attr.Expr.(*hclsyntax.LiteralValueExpr)
	if !ok {
		return false
	}
	return lit.Val.Equals(cty.True).True()
}

func itemRange(item bodyItem) hcl.Range {
	if item.attr != nil {
		return item.attr.SrcRange
	}
	return item.block.Range()
}
