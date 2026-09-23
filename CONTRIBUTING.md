# Contributing to IdemCheck

Thanks for helping. This project tests race conditions, so its own code has
to be squeaky clean under the race detector.

## Ground rules

- Scope: checking that an Idempotency-Key implementation survives retries
  and concurrent duplicates. Features outside that sentence (dashboards,
  observability platforms, chaos engineering) will be declined.
- Correctness > simple UX > reproducibility > maintainability > cleverness.
- Comments explain **why**, especially around concurrency. No filler comments.

## Development loop

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

All five must pass before a PR is ready. `go test -race ./...` is
non-negotiable — a tool that hunts races must not have any.

`go test ./...` also executes the reference fixtures A–J against
[docs/TEST_MATRIX.md](docs/TEST_MATRIX.md). If a verdict change makes them
fail, fix the code *and* the matrix expectations in the same PR — they may
not drift apart.

### Manual check

```bash
docker compose -f examples/docker-compose.yml up -d
go run ./cmd/idemcheck test --url http://localhost:8081/orders --body '{"item_id":42,"qty":1}'   # PASS
go run ./cmd/idemcheck test --url http://localhost:8082/orders --body '{"item_id":42,"qty":1}'   # FAIL (race)
```

Replace `go run` with a built binary if you prefer. On PowerShell, prefer
`--body-file` over inline `--body`: PowerShell rewrites quotes in inline JSON.

## Pull requests

- One focused change per PR.
- Tests required for behavior changes (unit; integration when it touches
  the engine or examples).
- Keep the dependency list small. Adding a dependency needs a justification
  in the PR description.
- Result JSON (`--format json`) is a stable contract: changing existing
  fields needs a note in the PR; prefer adding new fields. Exit codes and
  `summary.result` values are contract too.
- Release process (design only): [docs/RELEASE.md](docs/RELEASE.md).

## Reporting bugs

Include the exact command, the output (sanitize secrets from `-H` headers!),
your OS, and `idemcheck version`.
