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
// This finds every type under clients/ whose OnEvent delegates to something
// else's OnEvent, and requires one of them.
func TestForwardingSubscribersReportWhatTheyForwardTo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "clients"))
	require.NoError(t, err)

	// delegating[type] is the file it was found in; answered is the set of
	// types declaring either acceptable answer.
	delegating := map[string]string{}
	answered := map[string]bool{}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
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
