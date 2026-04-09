package procedure

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
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

	for _, item := range items {
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
			if _, ok := stmt.(*Return); ok {
				terminated = true
			}
		} else {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block",
				Detail:   "Control-flow blocks are not yet supported in procedures.",
				Subject:  item.block.DefRange().Ptr(),
			})
			return nil, diags
		}
	}

	return stmts, diags
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
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unexpected statement",
			Detail:   "break is not yet supported in procedures.",
			Subject:  attr.SrcRange.Ptr(),
		}}

	case "continue":
		if loopDepth == 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Continue outside loop",
				Detail:   "continue can only be used inside a while or range loop.",
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unexpected statement",
			Detail:   "continue is not yet supported in procedures.",
			Subject:  attr.SrcRange.Ptr(),
		}}

	default:
		return &Assignment{
			Name:     attr.Name,
			Expr:     attr.Expr,
			SrcRange: attr.SrcRange,
		}, nil
	}
}

func itemRange(item bodyItem) hcl.Range {
	if item.attr != nil {
		return item.attr.SrcRange
	}
	return item.block.Range()
}
