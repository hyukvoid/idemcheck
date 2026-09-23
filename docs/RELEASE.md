# Release design

Status: **design only.** Nothing described here is implemented or tagged.
`v0.1.3` is the latest release; no new tag may be cut until the validation
suite below is green on the release commit.

## Goals

- `go install github.com/hyukvoid/idemcheck/cmd/idemcheck@<tag>` works
  against the public Go module proxy.
- Every GitHub release carries prebuilt binaries plus SHA256 checksums.

## Binary matrix

| OS | Arch |
|---|---|
| windows | amd64, arm64 |
| linux   | amd64, arm64 |
| darwin  | amd64, arm64 |

Six artifacts, named `idemcheck_<version>_<os>_<arch>` (`.exe` on
windows), plus one `SHA256SUMS` file.

## Steps (when a release is actually cut)

1. Green validation on the release commit: `gofmt -l .`, `go vet ./...`,
   `go test ./...`, `go test -race ./...`, `go build ./...`, and the
   reference fixture matrix repeated (`go test ./internal/engine/
   -run TestReferenceFixturesMatrix -count=10`).
2. Decide the version (semver; a breaking change to the `--format json`
   contract or exit codes bumps the major version).
3. Create an annotated tag `vX.Y.Z`. Existing tags are immutable — never
   move or retag one.
4. Build the six artifacts. Version embedding needs no `-ldflags`:
   `internal/buildinfo` reads Go build info, so a tagged checkout reports
   the tag itself.
5. Generate `SHA256SUMS` (`sha256sum` on Linux, `shasum -a 256` on macOS,
   `certutil -hashfile <file> SHA256` on Windows for verification).
6. Draft the GitHub release from the tag, attach artifacts and checksums,
   paste the changelog section.
7. Verify: `go install github.com/hyukvoid/idemcheck/cmd/idemcheck@vX.Y.Z`
   in a clean module, then `idemcheck version` must print the tag.

## Tooling: script vs GoReleaser

- **Plain script** — six `go build` calls plus archive and checksum steps.
  Zero extra dependencies, fully auditable; the matrix is maintained by
  hand.
- **GoReleaser** — config-driven matrix, checksums, and changelog
  generation out of the box; adds a tool dependency and a config file.

Decision deferred: evaluate GoReleaser when the first release under this
design is cut. This document adopts neither.

## Explicit non-goals (v1.0)

- Homebrew, Scoop, or other package-manager formulas
- Container images
- Signed artifacts (revisit after v1.0)
- Automated changelog or release-note bots
