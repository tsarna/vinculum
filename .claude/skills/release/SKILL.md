---
name: release
description: Cut a Vinculum release — choose the version, prepare the changelog and version-bearing files, run the preflight checks, tag, and finish the post-release chores. Use when asked to cut, prepare, or ship a release, or to tag a new version.
---

# Cutting a Vinculum release

Publishing is automated: pushing a `v*` tag builds binaries, a Homebrew cask,
three container images, and the release notes. What is *not* automatic is the
judgment before the tag and the chores after it. This skill covers those.

**Most consistency checks are mechanized — do not re-check them by hand.**
`internal/release` (run as `TestReleaseConsistency`) verifies the changelog
structure, the link definitions, the version `doc/` promises, and the pinned
versions, and the release workflows run it against the tag before publishing
anything. Trust it; your job is the parts it cannot decide.

## 1. Choose the version

The default is already written down: `doc/` pages carry
`> **Changed in X.Y.Z.**` notes naming the next version. Find them with

```
grep -rn "in [0-9]\+\.[0-9]\+\.[0-9]\+" doc/ | grep -v "$(git describe --tags --abbrev=0 | tr -d v)"
```

Ask the user to confirm, and raise it if the Unreleased section warrants:

- **Breaking changes** — search the Unreleased section for `BREAKING`. While
  the project is pre-1.0 these ship in a minor bump, but they must be called
  out in the release notes.
- **Deprecations coming due** — read `doc/deprecations.md`. Anything whose
  planned removal has arrived is either removed in this release (a `Removed`
  section, and the entry moved to "Removed behavior") or the entry's planned
  removal is pushed out. Silently shipping past it is the failure mode.

If the chosen version differs from what `doc/` promises, update those notes
first — the preflight will fail on the mismatch, which is the point.

## 2. Prepare the tree

One commit, on `main`:

- **CHANGELOG.md** — rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`
  (today's date), leave a fresh empty `## [Unreleased]` above it, and update the
  link definitions at the bottom: `[Unreleased]` compares from the new version,
  and a new `[X.Y.Z]` line compares from the previous one.
- **Review the notes themselves.** They become the GitHub release body verbatim.
  Entries should read against the *last release*, not against earlier unreleased
  work — an entry describing a change to something that never shipped should be
  folded into the feature it belongs to.
- **Bump the pinned versions** — `doc/schema.md`'s sample `vinculumVersion` and
  `testdata/plugin-smoke/go.mod`. The check names both if you miss one.

Then, from the repo root:

```
VINCULUM_RELEASE_VERSION=vX.Y.Z go test ./internal/release/
gofmt -l . && go build ./... && go test ./...
go run . schema --strict --require-docs -o /dev/null
go run . schema --format markdown --check doc/
```

The first line is the release-specific gate; the rest is what CI runs anyway.

## 3. Tag

Push the prep commit to `main` and let CI go green **before** tagging — a tag on
a red commit means deleting and re-pushing a tag.

```
git tag vX.Y.Z
git push origin vX.Y.Z
```

Both tag-triggered workflows (`release.yml`, `docker.yml`) run the same
preflight first, so a tag that disagrees with the tree publishes nothing.

## 4. Watch it land

**Never create the GitHub Release by hand.** GoReleaser creates *and publishes*
it (no `draft:`, and `prerelease: auto` marks one only for a suffixed tag like
`v1.0.0-rc1`), attaching the archives, checksums, and `schema.json`. Its own
changelog generation is disabled, so the workflow then sets the body from the
CHANGELOG section with `gh release edit`. A hand-made release would take the tag
GoReleaser expects to create and fail the run.

| Workflow | Produces |
|---|---|
| Release | the GitHub Release itself, with binaries + checksums, Homebrew cask, `schema.json`, and the body set from the changelog section |
| Build and Push Docker Images | `vinculum`, `vinculum:*-minimal`, `vinculum-build` for amd64+arm64, then the plugin smoke gate that proves a plugin still builds and loads |
| Build and publish VCL index | `vinculum-index.tar.gz`, attached once GoReleaser publishes the release (it triggers on `release: published`) |

The plugin smoke gate is the one that catches cgo/ABI regressions, and it runs
*after* the images are pushed — so a failure there means the images are already
public and need a follow-up release, not a retry.

## 5. After the release

- **Bump the sibling plugin repo.** `~/src/vinculum-plugin-example` pins
  `github.com/tsarna/vinculum` in its `go.mod`; update it to the new version,
  run `go mod tidy`, and confirm it still builds. It is the worked example
  plugin authors copy, so a stale pin teaches the wrong version.
- **Check the Homebrew cask** landed in `tsarna/homebrew-tap`.
- **Confirm the release notes** rendered — the workflow sets them from the
  changelog section after GoReleaser creates the release.

## If a tag has to be redone

Nothing is published before preflight passes, so a preflight failure is safe to
recover from:

```
git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z
```

Fix, commit, and re-tag. Once images or a release exist, prefer a new patch
version over moving a tag — a moved tag poisons the Go module checksum database
for anyone who already fetched it.
