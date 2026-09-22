# IdemCheck

[![CI](https://github.com/hyukvoid/idemcheck/actions/workflows/ci.yml/badge.svg)](https://github.com/hyukvoid/idemcheck/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](https://github.com/hyukvoid/idemcheck/blob/main/go.mod)
[![License](https://img.shields.io/github/license/hyukvoid/idemcheck)](LICENSE)
[![Release](https://img.shields.io/github/v/release/hyukvoid/idemcheck)](https://github.com/hyukvoid/idemcheck/releases/latest)

**Break your idempotency implementation before production does.**

A CLI that stress-tests `Idempotency-Key` implementations against retries and
concurrent duplicate requests.

```text
$ idemcheck test \
    --url http://localhost:8082/orders \
    --body-file examples/request.json

IdemCheck v0.1.3

Target
POST http://localhost:8082/orders

Key Header
Idempotency-Key

──────────────────────────────────

Sequential retry ×2 ....................... PASS
Sequential retry ×10 ...................... PASS
Concurrent retry ×10 ...................... FAIL
Same key + changed payload ................ PASS
Different key + same payload .............. PASS

──────────────────────────────────

RACE CONDITION DETECTED

10 concurrent requests
1 idempotency key
10 unique semantic responses

...

Differing fields:
  $.order_id

...
Result:
FAILED
```

Same request.
Same Idempotency-Key.
Sent concurrently.

IdemCheck checks whether your API still behaves like one operation.

> **Scope:** IdemCheck validates observable HTTP response-level idempotency
> behavior. It does not prove that every downstream side effect was
> deduplicated.

## A real failure

The output above is an unedited run against the unsafe demo API on port
`8082`. Ten requests share one key; each response carries a different
`order_id`, so the endpoint created ten resources where there should have
been one:

```text
Fingerprint A ×1
  status: 201
  $.order_id: 975

Fingerprint B ×1
  status: 201
  $.order_id: 984

Fingerprint C ×1
  status: 201
  $.order_id: 976

...

Differing fields:
  $.order_id

10 unique semantic responses observed

Re-run:

idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --key idemcheck-88ce449cded4

──────────────────────────────────

Result:
FAILED
```

Sequential retries pass because the replay path is correct — only the
concurrent burst exposes the check-then-insert race. Exit code is `1`, and
the printed `--key` reproduces the same run.

## Install

```bash
go install github.com/hyukvoid/idemcheck/cmd/idemcheck@v0.1.3
```

Or always get the newest release:

```bash
go install github.com/hyukvoid/idemcheck/cmd/idemcheck@latest
```

Both commands were verified against the public Go module proxy; each
installs a binary that reports `IdemCheck v0.1.3`.

Build from source:

```bash
git clone https://github.com/hyukvoid/idemcheck
cd idemcheck
go build ./...
go build -o bin/idemcheck ./cmd/idemcheck
```

## 60-second demo

The repository ships two toy order APIs: a safe one on `:8081` and an unsafe
one on `:8082`.

```bash
git clone https://github.com/hyukvoid/idemcheck
cd idemcheck
docker compose -f examples/docker-compose.yml up -d
```

Run against the unsafe API — expect `FAIL` and exit code `1`:

```bash
idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json
```

```text
Sequential retry ×2 ....................... PASS
Sequential retry ×10 ...................... PASS
Concurrent retry ×10 ...................... FAIL
Same key + changed payload ................ PASS
Different key + same payload .............. PASS

──────────────────────────────────

RACE CONDITION DETECTED
...
Result:
FAILED
```

Run against the safe API — expect `PASS` and exit code `0`:

```bash
idemcheck test \
  --url http://localhost:8081/orders \
  --body-file examples/request.json
```

```text
Sequential retry ×2 ....................... PASS
Sequential retry ×10 ...................... PASS
Concurrent retry ×10 ...................... PASS
Same key + changed payload ................ PASS
Different key + same payload .............. PASS

──────────────────────────────────

Result:
PASS
```

Stop the demo:

```bash
docker compose -f examples/docker-compose.yml down
```

> **PowerShell:** prefer `--body-file` over inline `--body`; PowerShell
> mangles inline JSON quoting.

## What IdemCheck checks

| Check | Requests | Passes when |
|---|---|---|
| Sequential retry ×2 | Same key, sent one after another | One semantic response |
| Sequential retry ×10 | Same key, sent one after another | One semantic response |
| Concurrent retry ×10 | Same key, released through a barrier | One semantic response |
| Same key + changed payload | Same key, different body | Conflict is rejected or the original response is replayed (classified, not assumed) |
| Different key + same payload | Two different keys, same body (control) | Endpoint treats different keys as distinct requests |

Each check uses its own derived idempotency key, so one scenario cannot
contaminate another. A failed baseline request (HTTP ≥ 400) errors the run
out with exit code `2` instead of guessing.

Responses are compared as **semantic fingerprints**, not raw bytes:

```text
HTTP response → parse → drop ignored JSON paths → canonical JSON
             → + status + filtered headers → SHA-256
```

Key order never matters. Noise headers (`date`, `x-request-id`,
`traceparent`, `x-amzn-trace-id`, `cf-ray`, `server-timing`) are ignored by
default. Non-JSON bodies fall back to normalized raw comparison; malformed
JSON never crashes the run.

## Why concurrency matters

Sequential retries replay a stored response; they never overlap. A
check-then-insert race only appears when two requests evaluate the key
before either one has stored its result:

```text
UNSAFE
------

check key
  ↓
small delay / work
  ↓
create resource
  ↓
store idempotency result

Concurrent requests can all pass the initial check before the first one
stores its result.
```

```text
SAFE
-----

idempotency handling is synchronized / atomic
  ↓
same key resolves to the same logical result
```

The demo APIs implement exactly these two patterns. The unsafe handler
checks the key, sleeps, inserts the order, then records the key — nothing
serializes the window between check and insert. The safe handler takes a
per-key mutex: the first writer creates the order and stores the response,
every other request waits and then replays the stored bytes.

A loop of `curl` sends one request at a time and cannot produce that overlap.
IdemCheck starts all workers first, then releases them through a single
synchronization barrier so requests begin as close together as the OS allows.
With `--verbose` you can see how tightly the burst launched:

```text
Timings — Concurrent retry ×10 (ms after barrier release | latency):
  worker  0: +  0 | 251
  worker  1: +  0 | 261
  worker  2: +  0 | 252
  worker  3: +  0 | 261
  worker  4: +  0 | 253
  worker  5: +  0 | 261
  worker  6: +  0 | 253
  worker  7: +  0 | 253
  worker  8: +  0 | 253
  worker  9: +  0 | 253
  spread: all requests initiated within 0 ms of barrier release
```

Offsets are recorded in whole milliseconds: a displayed `0 ms` spread means
every request started within the same millisecond of barrier release — an
observed launch spread below 1 ms, not a claim that requests left at the
identical instant.

## CLI examples

```bash
# See every flag
idemcheck test --help
```

```text
IdemCheck sends sequential and concurrent duplicate requests sharing one
Idempotency-Key and reports whether the endpoint produces more than one
semantic response — the signature of an idempotency race condition.

Usage:
  idemcheck test [flags]

Flags:
      --allow-remote                permit testing a non-local host
      --body string                 request body (inline)
      --body-file string            request body from file
      --concurrency int             concurrent request count (default 10)
      --config string               YAML config file (response ignore lists)
      --format string               output format: text or json (default "text")
  -H, --header stringList           extra header "Name: value" (repeatable)
  -h, --help                        help for test
      --ignore-header stringArray   header name to ignore, repeatable (e.g. x-custom-trace)
      --ignore-json stringArray     JSON path to ignore, repeatable (e.g. $.request_id)
      --key string                  idempotency key (generated when omitted; set for deterministic repro)
      --key-header string           idempotency key header name (default "Idempotency-Key")
      --max-concurrency int         safety ceiling for --concurrency (default 50)
      --max-repeat int              safety ceiling for --repeat (default 100)
      --method string               HTTP method (default "POST")
      --repeat int                  sequential repeat count (default 10)
      --timeout duration            per-request HTTP timeout (default 10s)
      --url string                  target URL (required)
  -v, --verbose                     show per-request timing detail
```

Common recipes:

```bash
# Authenticated endpoint
idemcheck test \
  --url http://localhost:8080/orders \
  --body-file request.json \
  -H "Authorization: Bearer $TOKEN"

# Deterministic re-run of a reported failure
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --key idemcheck-88ce449cded4

# Config file + verbose timings
idemcheck test --config examples/idemcheck.yaml \
  --url http://localhost:8081/orders \
  --body-file examples/request.json --verbose

# Heavier burst (capped at --max-concurrency)
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --concurrency 25 --repeat 20
```

Exit codes:

| Code | Meaning |
|---|---|
| `0` | Pass (warnings allowed) |
| `1` | Idempotency violation detected |
| `2` | Configuration or execution error |

## JSON / CI usage

`--format json` emits a stable document with the same information as the
text report:

```bash
idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --format json > idemcheck.json
echo "exit=$?"   # 0 pass, 1 violation, 2 error
```

The top level contains `tool`, `version`, `target`, `summary`, `checks`,
`violations`, `evidence`, and `reproduce`. `summary` (trimmed from a real
run):

```json
{
  "result": "FAILED",
  "exit_code": 1,
  "checks_passed": 4,
  "checks_failed": 1,
  "checks_warned": 0,
  "checks_skipped": 0
}
```

`violations` (trimmed from a real run):

```json
[
  {
    "check": "concurrent",
    "type": "concurrent_race",
    "message": "10 concurrent requests\n1 idempotency key\n10 unique semantic responses",
    "differing_fields": [
      "$.order_id"
    ]
  }
]
```

Each `evidence` entry names one fingerprint group with its status and the
fields that differ; `reproduce.command` is a ready-to-paste re-run.

Warnings do not change the exit code: a run with `checks_warned > 0` still
exits `0` unless a check actually failed. In CI, treat exit code `1` as a
test failure and `2` as a configuration problem.

## Ignore rules

Ignore rules silence volatile fields before fingerprinting:

```yaml
# examples/idemcheck.yaml
#
# Ignore volatile noise before fingerprinting. Only ignore fields that carry
# no business meaning — never ignore identifiers such as $.order_id, or a
# real idempotency race will be reported as PASS.
response:
  ignore_json:
    - $.request_id
    - $.timestamp
  ignore_headers:
    - x-custom-trace
```

Paths use JSONPath-style prefixes (`$.request_id`, `$.meta.trace_id`,
`$.items[*].ts`). Matched object keys are deleted; matched array elements
become `null` so array shape stays comparable. The same rules are available
as flags: `--ignore-json '$.request_id' --ignore-header x-custom-trace`.

> **Ignore rules can suppress real violations.** Ignoring request IDs or
> timestamps removes harmless noise, but ignoring a business identifier such
> as `$.order_id` hides the very difference an idempotency bug produces.
> Running the unsafe demo with `--ignore-json '$.order_id'` turns its race
> from `FAIL` into `PASS` — and simultaneously degrades the different-keys
> control check to `WARN`, because responses for distinct keys then look
> identical. Review every ignore rule as carefully as the test itself.

## Safety guard

IdemCheck intentionally sends duplicate POST requests, so it refuses
non-local targets unless you opt in:

```console
$ idemcheck test --url http://api.example.invalid/orders --body-file examples/request.json
Error: refusing to test non-local target "api.example.invalid": this tool intentionally sends duplicate POST requests.
Re-run with --allow-remote if api.example.invalid is a test/staging environment you own
```

The command exits `2` before any request is sent. `localhost`, `127.0.0.0/8`
and `::1` are always allowed. With `--allow-remote` the run proceeds and
prints a loud warning first:

```text
WARNING: --allow-remote set; hammering 172.26.112.1 with duplicate requests. Make sure it is not production.
```

Only pass `--allow-remote` to test or staging systems you own. Two ceilings
bound the blast radius: `--max-concurrency` (default `50`) and
`--max-repeat` (default `100`); larger values are rejected with exit code
`2`.

## Limitations

- **Response-level scope.** IdemCheck validates observable HTTP
  response-level idempotency behavior. It does not prove that every
  downstream side effect was deduplicated — database rows, queue messages,
  emails, payments, and other effects are outside its view. It is a
  black-box HTTP tool; it never inspects your storage or consumers.
- **Passing is evidence, not proof.** A `PASS` means these runs observed one
  semantic response, not that no interleaving anywhere could produce two.
- **Structural detection.** Differing fields are found by structural JSON
  diff. A resource ID embedded in an unstructured text string cannot be
  named as a differing field; the fingerprint still differs and the check
  still fails, but the report shows fingerprint-level evidence only.
- **Ignore rules can hide failures.** See the warning above.

## Architecture

```text
cmd/idemcheck/        CLI entry point
internal/cli/         flag parsing + terminal rendering
internal/config/      options, safety guard, YAML config
internal/engine/      sequential + barrier runners, scenario matrix
internal/fingerprint/ normalization, ignore paths, JSON diff
internal/httpx/       request building
internal/models/      result schema (JSON contract)
internal/report/      assemble + write text/JSON output
internal/buildinfo/   version from Go build info
examples/             safe + unsafe demo APIs, compose file, sample config
integration/          end-to-end tests (unsafe races, safe passes)
```

Workers are prepared first, then released together; results land in
per-worker slots so the hot path takes no locks. Each response flows through
the fingerprint pipeline above, and every scenario derives its own key from
the base key so checks stay isolated.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development loop.

Issues and pull requests are welcome:
[bug report](https://github.com/hyukvoid/idemcheck/issues/new?template=bug_report.md) ·
[feature request](https://github.com/hyukvoid/idemcheck/issues/new?template=feature_request.md) ·
[security policy](SECURITY.md)

## License

[MIT](LICENSE)
