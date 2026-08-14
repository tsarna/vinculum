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

### Rehearse first when the pipeline itself changed

A `vX.Y.Z-rc1` tag runs the whole thing for real while touching nothing
permanent: `prerelease: auto` marks the release a prerelease, the cask's
`skip_upload: auto` leaves the Homebrew tap alone, and a prerelease tag does not
create or move `:X.Y`, `:X`, or `:latest`. Neither the preflight nor the notes
extraction needs the changelog renamed first — both strip the suffix, and the
notes fall back to `[Unreleased]`.

Worth the ten minutes whenever a release workflow, `.goreleaser.yaml`, or a
Dockerfile changed since the last tag. The 0.45.0 rehearsal found two breaks
that would otherwise have shipped, one of them silent (see step 4's index note).

Cleaning up afterwards takes three things, not one: `gh release delete
vX.Y.Z-rc1 --yes --cleanup-tag`, the local tag, and the container versions —
which need `gh auth refresh -s delete:packages,read:packages` first, then a
`DELETE` per version id from
`gh api user/packages/container/<pkg>/versions`.

## 4. Watch it land

**Never create the GitHub Release by hand.** GoReleaser creates *and publishes*
it (no `draft:`, and `prerelease: auto` marks one only for a suffixed tag like
`v1.0.0-rc1`), attaching the archives, checksums, and `schema.json`. Its own
changelog generation is disabled, so the workflow then sets the body from the
CHANGELOG section with `gh release edit`. A hand-made release would take the tag
GoReleaser expects to create and fail the run.

That release cannot trigger anything: GitHub suppresses workflow runs from
events raised with the default `GITHUB_TOKEN`, which is why the index is a job
Release *calls* rather than a workflow waiting on `release: published`. Anything
else that should follow a release has to be chained the same way — a new
`on: release` workflow would simply never fire.

| Workflow | Produces |
|---|---|
| Release | the GitHub Release itself, with binaries + checksums, Homebrew cask, `schema.json`, and the body set from the changelog section |
| Build and Push Docker Images | `vinculum`, `vinculum:*-minimal`, `vinculum-build` for amd64+arm64, then the plugin smoke gate that proves a plugin still builds and loads |
| Build and publish VCL index | `vinculum-index.tar.gz`, attached by the `index` job Release calls after creating the release |

The plugin smoke gate is the one that catches cgo/ABI regressions, and it runs
*after* the images are pushed — so a failure there means the images are already
public and need a follow-up release, not a retry.

A transient `proxy.golang.org` failure inside a Docker leg is worth a re-run
(`gh run rerun <id> --failed`) before looking for a real cause; it has happened.

## 5. Release the plugin example

`~/src/vinculum-plugin-example` is a **second release, not a chore**: its tags
mirror vinculum's, it has its own GitHub releases, and it is the worked example
plugin authors copy. Do it after the images exist, since it builds against them.

1. **Sync it** — it takes Renovate PRs, so pull before touching anything.
2. **Bump and re-pin.** `go get github.com/tsarna/vinculum@vX.Y.Z && go mod
   tidy`, then update the image tags **Renovate does not reach**: it maintains
   the `FROM` line in `Dockerfile`, and nothing maintains the two in `README.md`
   (the `FROM` in the deployment example, and `make docker-build
   VINCULUM_VERSION=`). Those had drifted two releases behind by 0.45.0. Leave
   historical sentences alone — "arrived in vinculum 0.43.0" is a fact, not a
   pin. The `Makefile` derives its version from `go.mod` and needs nothing.
3. **Verify against the released module, not the workspace.** `go.work` points
   the local build at `../vinculum`, so a plain `go build` proves nothing about
   the published version:

   ```
   GOWORK=off go mod tidy -diff
   GOWORK=off go build -buildmode=plugin -o /tmp/example.so .
   ```

   `make docker-build` is the faithful check — same image, same flags as a real
   deployment — but needs a running Docker. CI does it either way on push.
4. **Commit, push, and let CI pass** — it installs the just-released vinculum
   and loads the plugin, which is the real ABI check.
5. **Tag and release.** There is no release workflow here, so the release is
   made by hand, and the body is one line:

   ```
   git tag vX.Y.Z && git push origin vX.Y.Z
   gh release create vX.Y.Z --title "vX.Y.Z" --notes "Track Vinculum X.Y.Z"
   ```

## 6. Finally

- **Check the Homebrew cask** landed in `tsarna/homebrew-tap` at the new version.
- **Confirm the release notes** rendered, and that `vinculum-index.tar.gz` is
  among the assets — it is the one asset added by a separate job.

## If a tag has to be redone

Nothing is published before preflight passes, so a preflight failure is safe to
recover from:

```
git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z
```

Fix, commit, and re-tag. Once images or a release exist, prefer a new patch
version over moving a tag — a moved tag poisons the Go module checksum database
for anyone who already fetched it.
