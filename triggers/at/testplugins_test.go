package at_test

// Import functions package to trigger init() registrations during testing,
// making time-cty-funcs functions (now(), timeadd(), duration(), etc.)
// available in eval contexts.
import _ "github.com/tsarna/vinculum/functions"
