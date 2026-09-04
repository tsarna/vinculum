package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A subscriber that forwards OnEvent to another subscriber has to say what it
// forwards to, or a settle point asking what a delivery's return will mean gets
// the wrapper's own silence instead of the answer.
//
// The consequence is silent and expensive. A `client "kafka"` sender with
// produce_mode = "async" returns before the broker has the record and declares
// itself deferred; a proxy in front of it that answered for itself would have
// the inbound message acknowledged at the hand-off, and a produce that then
// failed would have nothing left to redeliver. No error, no log line — the only
// symptom is messages disappearing on a path documented as at-least-once.
//
// That is exactly the shape a test has to hold, because nothing else will. Two
// answers are acceptable:
//
//   - Unwrap() bus.Subscriber, for a wrapper standing in front of exactly one
//     subscriber. DispositionOf walks it.
//   - DeliveryDisposition() bus.Disposition, for a fan-out, where there are N
//     subscribers behind this one and a single-valued Unwrap cannot say so.
//
// This walks the whole repository — every package, not just clients/ — and
// finds each type whose OnEvent delegates to something else's, or which embeds
// a bus interface that forwards for it. Each must declare one of the two
// answers. A wrapper is worth writing wherever it is useful, so the scan goes
// wherever one might live.
func TestForwardingSubscribersReportWhatTheyForwardTo(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	// delegating[type] is the file it was found in; answered is the set of
	// types declaring either acceptable answer.
	delegating := map[string]string{}
	answered := map[string]bool{}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "examples":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		// A struct embedding the bus.Subscriber or bus.EventBus *interface*
		// forwards every delivery to whatever is inside it, and has no OnEvent
		// of its own for the scan below to find — the method is promoted. It
		// does not promote DeliveryDisposition, though, because that belongs to
		// the concrete subscriber and is not in either interface. So it answers
		// for itself, silently, and says the work is done.
		//
		// Embedding bus.BaseSubscriber is the opposite case and is fine: that is
		// a struct of no-ops, forwarding nothing.
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, field := range st.Fields.List {
					if len(field.Names) != 0 {
						continue // named field, not embedded
					}
					if embedsForwardingInterface(field.Type) {
						delegating[ts.Name.Name] = mustRel(t, root, path)
					}
				}
			}
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}

			receiver := receiverTypeName(fn.Recv.List[0].Type)
			if receiver == "" {
				continue
			}

			switch fn.Name.Name {
			case "Unwrap", "DeliveryDisposition":
				answered[receiver] = true
			case "OnEvent":
				if fn.Body != nil && callsOnEvent(fn.Body) {
					delegating[receiver] = mustRel(t, root, path)
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, delegating,
		"the scan found no forwarding subscribers at all, which means it has stopped working")

	var offenders []string
	for typeName, path := range delegating {
		if !answered[typeName] {
			offenders = append(offenders, path+": "+typeName)
		}
	}
	sort.Strings(offenders)

	require.Empty(t, offenders,
		"these forward OnEvent to another subscriber but answer for themselves, so a "+
			"deferring or observing subscriber behind them is invisible to every settle "+
			"point. Add Unwrap() bus.Subscriber, or DeliveryDisposition() for a fan-out:\n  %s",
		strings.Join(offenders, "\n  "))
}

// embedsForwardingInterface reports whether an embedded field is one of the bus
// interfaces whose promoted OnEvent hands the delivery to something else.
//
// bus.BaseSubscriber is deliberately not in this set: it is a struct of no-ops,
// so a type embedding it forwards nothing and answering for itself is correct.
func embedsForwardingInterface(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "bus" {
		return false
	}
	return sel.Sel.Name == "Subscriber" || sel.Sel.Name == "EventBus"
}

// receiverTypeName returns the bare type name of a method receiver, following
// one level of pointer.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// callsOnEvent reports whether body calls OnEvent on anything. That is the
// signature of forwarding: a leaf that does the work itself has no reason to.
func callsOnEvent(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "OnEvent" {
			found = true
			return false
		}
		return true
	})
	return found
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	require.NoError(t, err)
	return rel
}
