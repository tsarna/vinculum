package redisstream_test

// Import functions package to trigger init() registrations during testing,
// mirroring what cmd/plugins.go does for the main binary. Without it these
// tests build configs against a function namespace the real binary never has:
// the dead-letter test's `action = assert(false, ...)` calls a function that
// was not registered here, which the deferred-reference check now reports.
import _ "github.com/tsarna/vinculum/functions"
