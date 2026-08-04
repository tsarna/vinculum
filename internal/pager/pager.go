// Package pager writes long output through the user's terminal pager.
//
// The conventions are git's rather than anything invented here: VINCULUM_PAGER
// then PAGER then less; a sensible default $LESS when the user has not set one;
// and no pager at all when output is not going to a terminal, so that piping
// and redirection behave the way every other command's does.
package pager

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Options configures Page.
type Options struct {
	// Disabled skips the pager entirely, whatever the environment says.
	Disabled bool

	// Getenv looks up environment variables. Nil means os.Getenv; tests
	// supply their own so they need not mutate the process environment.
	Getenv func(string) string
}

func (o Options) getenv(name string) string {
	if o.Getenv != nil {
		return o.Getenv(name)
	}
	return os.Getenv(name)
}

// Page writes text through the user's pager, falling back to writing it
// directly to out.
//
// The fallback is unconditional on failure: losing the output because a pager
// could not be started would be a far worse outcome than not paging it.
func Page(out io.Writer, text string, opts Options) error {
	cmd := command(out, opts)
	if cmd == nil {
		return write(out, text)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return write(out, text)
	}
	if err := cmd.Start(); err != nil {
		return write(out, text)
	}

	// A reader who quits the pager early closes the pipe, so the write fails
	// with EPIPE. That is a normal way to finish reading, not an error.
	_, writeErr := io.WriteString(stdin, text)
	stdin.Close()
	waitErr := cmd.Wait()

	if writeErr != nil && !errors.Is(writeErr, syscall.EPIPE) {
		return writeErr
	}
	// The pager's own exit status is likewise not this program's result: less
	// exits non-zero when the reader quits during a search, and the output was
	// still delivered.
	_ = waitErr
	return nil
}

// command returns the pager process to run, or nil when the output should be
// written directly.
func command(out io.Writer, opts Options) *exec.Cmd {
	if opts.Disabled || !isTerminal(out) {
		return nil
	}

	name := pagerCommand(opts)
	if name == "" || name == "cat" {
		return nil
	}

	// Through a shell, because PAGER is conventionally a command line rather
	// than a program name — "less -R", or a pipeline.
	cmd := exec.Command("sh", "-c", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnv(name, opts)
	return cmd
}

// pagerCommand resolves which pager to run.
func pagerCommand(opts Options) string {
	for _, name := range []string{"VINCULUM_PAGER", "PAGER"} {
		if v := strings.TrimSpace(opts.getenv(name)); v != "" {
			return v
		}
	}
	return "less"
}

// childEnv supplies $LESS when the user has not, and returns nil (inherit)
// otherwise.
//
// The flag that matters is -F, quit-if-one-screen: without it, `vinculum man
// var` traps a reader in a pager to read four lines. -R passes through the
// colour the terminal sink emits, and -X keeps the text on screen after the
// pager exits, so a short page reads like ordinary command output.
func childEnv(pager string, opts Options) []string {
	if filepath.Base(strings.Fields(pager)[0]) != "less" {
		return nil
	}
	if opts.getenv("LESS") != "" {
		return nil
	}
	return append(os.Environ(), "LESS=FRX")
}

// isTerminal reports whether out is a terminal. Only an *os.File can be one,
// which is also what makes a test's bytes.Buffer take the direct path.
func isTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func write(out io.Writer, text string) error {
	_, err := io.WriteString(out, text)
	return err
}
