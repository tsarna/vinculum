// Package version exposes build-time identity information about the
// vinculum binary.
//
// Version is typically injected at build time via:
//
//	go build -ldflags="-X github.com/tsarna/vinculum/version.Version=v1.2.3"
//
// Commit, BuildTime, and Modified are populated automatically from
// runtime/debug VCS stamping when available (i.e. when built from a git
// checkout with the .git directory present). Docker builds don't have
// access to .git, so the Dockerfiles inject all fields explicitly via
// ldflags instead.
package version

import "runtime/debug"

// Version is the release tag. Defaults to "dev" for local builds.
var Version = "dev"

// Commit is the git commit SHA, or empty if unknown.
var Commit = ""

// BuildTime is the build/commit timestamp in RFC3339 format, or empty if unknown.
var BuildTime = ""

// Modified is true if the working tree had uncommitted changes at build time.
var Modified = false

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if Commit == "" {
				Commit = s.Value
			}
		case "vcs.time":
			if BuildTime == "" {
				BuildTime = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				Modified = true
			}
		}
	}
}

// ShortCommit returns the first 12 characters of Commit, or Commit if shorter.
func ShortCommit() string {
	if len(Commit) > 12 {
		return Commit[:12]
	}
	return Commit
}

// String returns a human-readable one-line description of the build.
func String() string {
	s := Version
	if Commit != "" {
		s += " (" + ShortCommit()
		if Modified {
			s += "-dirty"
		}
		s += ")"
	}
	return s
}
