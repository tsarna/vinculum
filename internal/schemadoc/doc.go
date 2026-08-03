// Package schemadoc renders the machine-readable configuration-language schema
// that `vinculum schema` produces (config.SchemaDocument) into documentation a
// person reads.
//
// It is the single renderer behind three front doors — the `vinculum man`
// command, the REPL's `:man`, and the VCL `help()` builtin — and behind the
// Markdown projection that generates the derivable parts of doc/. They differ
// only in which subtree they render and which sink they render into, so the
// package is organized as a pipeline:
//
//	path []string ──► resolver ──► Node ──► Walk ──► []Event ──► sink
//
// The event vocabulary exists rather than a Printf per renderer because the
// sinks cannot share an output form: the Markdown sink emits GFM, the terminal
// sink emits ANSI wrapped to the terminal width, and the plain sink feeds
// help(), which returns a cty.String and so can carry neither.
//
// Nothing here re-derives what the schema already knows. If a page wants to say
// something this package cannot, that is a gap in the curated metadata next to
// the decode struct (see config/schema.go and doc/schema.md), not prose to
// hand-write into a renderer.
package schemadoc
