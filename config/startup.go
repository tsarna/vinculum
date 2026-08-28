package config

import "errors"

// TerminalError marks a startup failure that retrying cannot fix — a port
// already in use, an unregistered driver, an unparseable DSN, a file that is
// not there. The boot loop stops, runs teardown, and exits non-zero rather than
// leaving a process running that can never do its job.
//
// It is consulted **during boot only**. A component that discovers mid-life
// that its credentials were revoked reports itself not-ready and keeps trying;
// it does not take the process down. An exit path that can fire at 3am from a
// remote service's state change is the cascading-failure shape that readiness
// exists to avoid, and it would fire across every replica at once.
//
// Default to retriable. A TCP refusal is transient by nature and a 401 might be
// rotated credentials, so only a component that is *sure* wraps its failure
// here. Guessing wrong toward terminal turns a recoverable outage into a
// crash-loop; guessing wrong toward retriable costs an unready pod and a clear
// reason string, which is much cheaper.
type TerminalError struct{ Err error }

func (e *TerminalError) Error() string { return e.Err.Error() }

func (e *TerminalError) Unwrap() error { return e.Err }

// Terminal marks err as unrecoverable. Nil-safe, so a caller can wrap the
// result of a call that may not have failed.
//
// The wrapper delegates Error and Unwrap, so a caller that inspects the cause
// with errors.As or errors.Is sees exactly what it saw before the wrapping —
// the same property config.Reported relies on.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &TerminalError{Err: err}
}

// IsTerminal reports whether err marks a failure retrying cannot fix. It looks
// through wrapping, so a component may add context to a terminal failure on the
// way out without losing the classification.
func IsTerminal(err error) bool {
	var t *TerminalError
	return errors.As(err, &t)
}
