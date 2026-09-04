package kafka_test

// Import the functions package to trigger init() registrations during testing,
// mirroring what cmd/plugins.go does for the main binary. Without it the
// settle tests build configs against a function namespace the real binary
// always has: `inbound::ack()` and `inbound::nack()` would be reported as
// calls into an unknown namespace rather than settling anything.
import _ "github.com/tsarna/vinculum/functions"
