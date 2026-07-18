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

// ModuleVersion returns the recorded module version of the dependency with the
// given module path — e.g. ModuleVersion("github.com/tsarna/functy") might yield
// "v0.11.0" in a released build. It reads runtime/debug build info, so it needs
// no ldflags or cooperation from the dependency: a library cannot inject its own
// version the way a main package does with -ldflags, but its module version rides
// along in the importing binary's build info regardless.
//
// It returns "(devel)" for a workspace or replace build (where no released version
// applies), and "" if the module is not a dependency or build info is unavailable.
// Note only the module *version* is recorded per dependency; a dependency's commit
// and build time are not — Go stamps VCS info (vcs.revision/vcs.time) for the main
// module only, so those describe this binary, not its dependencies.
func ModuleVersion(path string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path != path {
			continue
		}
		if dep.Replace != nil && dep.Replace.Version != "" {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return ""
}
