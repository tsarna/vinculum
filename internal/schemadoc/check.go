package schemadoc

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// man::check — whether a configuration loads, from inside a config.
//
// It completes the reference: a reader who looked a block up and wrote one can
// find out whether it parses, without a shell to run `vinculum check` in. The
// build is the one `vinculum check` does, and it answers with the same
// diagnostics, but the text being built is not the operator's, so the build is
// fenced:
//
//   - It is passed as bytes, and a bytes source is never read as a .vinit file,
//     so no plugin loads and no git block clones. For the same reason neither a
//     .vinit nor a .cty file can be checked.
//   - It sees an empty environment, since a diagnostic can quote a value.
//   - Its file functions are rooted at an empty directory made for it, so they
//     exist, and a config that uses them checks, but they read nothing real.
//   - Its user functions are limited in depth, since a recursion that overflows
//     the stack takes the whole process with it.
//   - It is bounded in size and in time, and only one runs at once.
//
// Some things building does are not fenced — a tls block reads the files it
// names — which is why an endpoint that offers this should not be public.

// checkFilename is what diagnostics call the submitted source.
const checkFilename = "config.vcl"

// The bounds on a check. Variables so that a test can lower them.
var (
	checkMaxSourceBytes = 256 << 10
	checkTimeout        = 10 * time.Second
	checkMaxCallDepth   = 500
	// checkQueueWait is how long a check waits for another to finish. Short,
	// and separate from checkTimeout: a check takes milliseconds, so a longer
	// wait means one is stuck, and a config under check that calls man::check
	// itself must not spend its own timeout waiting on its own slot.
	checkQueueWait = time.Second
)

// checkSlot admits one check at a time. A slot is given back when the build
// returns, not when its caller stops waiting: Build cannot be cancelled, so a
// check that timed out is still running, and counting it is what stops a caller
// who keeps retrying from piling builds up behind it.
var checkSlot = make(chan struct{}, 1)

var checkDiagnosticType = cty.Object(map[string]cty.Type{
	"severity":   cty.String,
	"summary":    cty.String,
	"detail":     cty.String,
	"line":       cty.Number,
	"column":     cty.Number,
	"end_line":   cty.Number,
	"end_column": cty.Number,
})

var checkResultType = cty.Object(map[string]cty.Type{
	"valid":       cty.Bool,
	"errors":      cty.Number,
	"warnings":    cty.Number,
	"diagnostics": cty.List(checkDiagnosticType),
	"text":        cty.String,
})

func manCheckFunc() function.Function {
	return function.New(&function.Spec{
		Description: `Check a configuration without running it, as ` + "`vinculum check`" + ` does, and return what it reports: an object with valid (false when anything is an error; warnings do not count), errors and warnings (counts), diagnostics (a list of objects with severity, summary, detail, line, column, end_line and end_column, the positions null for a problem with no place in the source), and text (every diagnostic with its line quoted, the form to show a reader). The source is one .vcl file, named config.vcl in what is reported. It cannot be a .vinit or .cty file, and it is checked alone, so a block defined in another file of the same configuration is reported as missing. The check sees an empty environment — env.* holds nothing, so a try(env.X, default) takes its default — and file functions rooted at an empty directory. A source over 256 KB, or one that takes longer than ten seconds to check, is refused. Only one check runs at a time, and one that waits more than a second for another to finish is refused too; each refusal is reported as an error diagnostic with no position, like any other problem.`,
		Params: []function.Parameter{{
			Name:        "source",
			Type:        cty.String,
			Description: "The text of one .vcl file",
		}},
		Type: function.StaticReturnType(checkResultType),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			return checkResult(check([]byte(args[0].AsString()))), nil
		},
	})
}

// checked is what one check found, and the files to quote it against.
type checked struct {
	diags hcl.Diagnostics
	files map[string]*hcl.File
}

// check builds source within the bounds described above.
func check(source []byte) checked {
	if len(source) > checkMaxSourceBytes {
		return refused("Configuration too large",
			fmt.Sprintf("The source is %d bytes, and a check accepts at most %d.", len(source), checkMaxSourceBytes))
	}

	queued := time.NewTimer(checkQueueWait)
	defer queued.Stop()
	select {
	case checkSlot <- struct{}{}:
	case <-queued.C:
		return refused("Check not run", "Another check is still running. Try again shortly.")
	}

	deadline := time.NewTimer(checkTimeout)
	defer deadline.Stop()

	done := make(chan checked, 1)
	go func() {
		defer func() { <-checkSlot }()
		defer func() {
			// A panic here is on a goroutine of its own, where it would end the
			// process. What was submitted is not the operator's, so it gets a
			// diagnostic instead.
			if p := recover(); p != nil {
				done <- refused("Check failed", fmt.Sprintf("The check stopped on an internal error: %v", p))
			}
		}()
		done <- checkBuild(source)
	}()

	select {
	case r := <-done:
		return r
	case <-deadline.C:
		return refused("Check did not finish",
			fmt.Sprintf("Checking the configuration took longer than %s. Look for an expression that does far more work than it needs to, such as nested for expressions over long lists.", checkTimeout))
	}
}

// checkBuild is build, as a variable so that a test can make one that is slow.
var checkBuild = build

// build is one fenced build of source, torn down again.
func build(source []byte) checked {
	dir, err := os.MkdirTemp("", "vinculum-check-")
	if err != nil {
		return refused("Check not run", "No scratch directory could be made for the file functions.")
	}
	defer os.RemoveAll(dir)

	builder := config.NewConfig().
		WithLogger(zap.NewNop()).
		WithSources(source).
		WithEnvironment([]string{}).
		WithMaxCallDepth(checkMaxCallDepth).
		WithFeature("readfiles", dir).
		WithFeature("writefiles", dir)

	cfg, diags := builder.Build()
	if cfg != nil {
		cfg.Discard()
	}
	return renamed(diags, builder.Files(), dir)
}

// renamed gives the source the name the reader knows it by. A bytes source is
// named for its address, which means nothing to anyone, and would otherwise
// appear in every range and in any message that quotes one. The scratch
// directory is replaced too, for the same reason.
func renamed(diags hcl.Diagnostics, files map[string]*hcl.File, dir string) checked {
	var old string
	for name := range files {
		if strings.HasPrefix(name, "<bytes@") {
			old = name
		}
	}
	r := checked{files: map[string]*hcl.File{}}
	if f := files[old]; f != nil {
		r.files[checkFilename] = f
	}

	rename := func(s string) string {
		if old != "" {
			s = strings.ReplaceAll(s, old, checkFilename)
		}
		return strings.ReplaceAll(s, dir, ".")
	}
	renameRange := func(rng *hcl.Range) *hcl.Range {
		if rng == nil {
			return nil
		}
		c := *rng
		c.Filename = rename(c.Filename)
		return &c
	}
	for _, d := range diags {
		c := *d
		c.Summary, c.Detail = rename(d.Summary), rename(d.Detail)
		c.Subject, c.Context = renameRange(d.Subject), renameRange(d.Context)
		r.diags = append(r.diags, &c)
	}
	return r
}

// refused is a check that could not give an answer about the configuration.
func refused(summary, detail string) checked {
	return checked{diags: hcl.Diagnostics{{Severity: hcl.DiagError, Summary: summary, Detail: detail}}}
}

// checkResult is man::check's object.
func checkResult(r checked) cty.Value {
	var errs, warns int64
	list := make([]cty.Value, 0, len(r.diags))
	for _, d := range r.diags {
		severity := "error"
		if d.Severity == hcl.DiagWarning {
			severity = "warning"
			warns++
		} else {
			errs++
		}
		pos := map[string]cty.Value{
			"line": cty.NullVal(cty.Number), "column": cty.NullVal(cty.Number),
			"end_line": cty.NullVal(cty.Number), "end_column": cty.NullVal(cty.Number),
		}
		if s := d.Subject; s != nil {
			pos["line"], pos["column"] = cty.NumberIntVal(int64(s.Start.Line)), cty.NumberIntVal(int64(s.Start.Column))
			pos["end_line"], pos["end_column"] = cty.NumberIntVal(int64(s.End.Line)), cty.NumberIntVal(int64(s.End.Column))
		}
		pos["severity"] = cty.StringVal(severity)
		pos["summary"] = cty.StringVal(d.Summary)
		pos["detail"] = cty.StringVal(d.Detail)
		list = append(list, cty.ObjectVal(pos))
	}

	diagList := cty.ListValEmpty(checkDiagnosticType)
	if len(list) > 0 {
		diagList = cty.ListVal(list)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"valid":       cty.BoolVal(errs == 0),
		"errors":      cty.NumberIntVal(errs),
		"warnings":    cty.NumberIntVal(warns),
		"diagnostics": diagList,
		"text":        cty.StringVal(checkText(r, errs, warns)),
	})
}

// checkText is the reader's form: a verdict, then each diagnostic with its
// line quoted, as `vinculum check` prints them.
func checkText(r checked, errs, warns int64) string {
	var b bytes.Buffer
	switch {
	case errs == 0 && warns == 0:
		b.WriteString("The configuration is valid.\n")
	case errs == 0:
		fmt.Fprintf(&b, "The configuration is valid, with %s.\n\n", count(warns, "warning"))
	default:
		fmt.Fprintf(&b, "The configuration is not valid: %s", count(errs, "error"))
		if warns > 0 {
			fmt.Fprintf(&b, " and %s", count(warns, "warning"))
		}
		b.WriteString(".\n\n")
	}
	if len(r.diags) > 0 {
		hcl.NewDiagnosticTextWriter(&b, r.files, 0, false).WriteDiagnostics(r.diags) //nolint:errcheck
	}
	return b.String()
}

func count(n int64, word string) string {
	return fmt.Sprintf("%d %s%s", n, word, plural(int(n)))
}
