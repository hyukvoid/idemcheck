# IdemCheck

[![CI](https://github.com/hyukvoid/idemcheck/actions/workflows/ci.yml/badge.svg)](https://github.com/hyukvoid/idemcheck/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](https://github.com/hyukvoid/idemcheck/blob/main/go.mod)
[![License](https://img.shields.io/github/license/hyukvoid/idemcheck)](LICENSE)
[![Release](https://img.shields.io/github/v/release/hyukvoid/idemcheck)](https://github.com/hyukvoid/idemcheck/releases/latest)

**Break your idempotency implementation before production does.**

IdemCheck stress-tests the observable `Idempotency-Key` contract of HTTP APIs
under retries and concurrent duplicate requests.

Black-box by design: it is especially useful for third-party, partner, and
vendor APIs whose internals you do not control and cannot inspect. It also
serves as an external contract check for your own services — a useful
second opinion that does not replace direct database, event, or payment
assertions when those are available.

```text
$ idemcheck test \
    --url http://localhost:8082/orders \
    --body-file examples/request.json

Target
POST http://localhost:8082/orders

Key Header
Idempotency-Key

──────────────────────────────────

Sequential retry ×2 ....................... PASS
  VERDICT: 2 requests converged on 1 logical result
Sequential retry ×10 ...................... PASS
  VERDICT: 10 requests converged on 1 logical result
Concurrent retry ×10 ...................... FAIL
  BURST: 10 requests released together (spread 0 ms)
  SETTLE: 250ms before replay
  REPLAY: 1 request -> HTTP 201 (confirmed a logical result)
  VERDICT: 10 distinct logical results for one idempotency key: response bodies diverge
Same key + changed payload ................ PASS
  VERDICT: modified payload (added "idemcheck_variant" field) replayed the original logical result (HTTP 201 -> 201)
Different key + same payload .............. PASS
  VERDICT: endpoint treats different keys as distinct requests: 2 unique semantic responses

──────────────────────────────────

RACE CONDITION DETECTED

11 concurrent requests
1 idempotency key
10 distinct logical results

...

Differing fields:
  $.order_id

...
Result:
FAIL

Concurrency trials: 1 observed
```

Same request.
Same Idempotency-Key.
Sent concurrently.

IdemCheck checks whether your API still behaves like one operation.

> **What PASS means:** no violation was observed within the configured
> HTTP-visible checks — not that the entire operation executed exactly
> once. Hidden side effects — a database insert, a message publish, an
> email, a payment capture — are not visible in HTTP responses, and
> IdemCheck never claims they were deduplicated. The exact
> false-positive / false-negative contract, fixture by fixture, is
> executed from [docs/TEST_MATRIX.md](docs/TEST_MATRIX.md).

## A real failure

The output above is a trimmed run against the unsafe demo API on port
`8082` (version line and fingerprint list shortened). The burst releases
ten requests sharing one key; each response carries a different
`order_id`, so the endpoint created ten resources where there should have
been one:

```text
Requests:
11

Unique semantic responses:
10

Fingerprint A ×2
  status: 201
  $.order_id: 812

Fingerprint B ×1
  status: 201
  $.order_id: 810

...

Differing fields:
  $.order_id

Re-run:

idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --key idemcheck-69ddd9ba2823

──────────────────────────────────

Result:
FAIL

Concurrency trials: 1 observed
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
  VERDICT: 2 requests converged on 1 logical result
Sequential retry ×10 ...................... PASS
  VERDICT: 10 requests converged on 1 logical result
Concurrent retry ×10 ...................... FAIL
  BURST: 10 requests released together (spread 0 ms)
  SETTLE: 250ms before replay
  REPLAY: 1 request -> HTTP 201 (confirmed a logical result)
  VERDICT: 10 distinct logical results for one idempotency key: response bodies diverge
Same key + changed payload ................ PASS
  VERDICT: modified payload (added "idemcheck_variant" field) replayed the original logical result (HTTP 201 -> 201)
Different key + same payload .............. PASS
  VERDICT: endpoint treats different keys as distinct requests: 2 unique semantic responses

──────────────────────────────────

RACE CONDITION DETECTED
...
Result:
FAIL

Concurrency trials: 1 observed
```

Run against the safe API — expect `PASS` and exit code `0`:

```bash
idemcheck test \
  --url http://localhost:8081/orders \
  --body-file examples/request.json
```

```text
Sequential retry ×2 ....................... PASS
  VERDICT: 2 requests converged on 1 logical result
Sequential retry ×10 ...................... PASS
  VERDICT: 10 requests converged on 1 logical result
Concurrent retry ×10 ...................... PASS
  BURST: 10 requests released together (spread 0 ms)
  SETTLE: 250ms before replay
  REPLAY: 1 request -> HTTP 201 (confirmed a logical result)
  VERDICT: 11 requests converged on 1 logical result (replay agreed)
Same key + changed payload ................ PASS
  VERDICT: payload conflict rejected: original HTTP 201, modified payload HTTP 409 (added "idemcheck_variant" field)
Different key + same payload .............. PASS
  VERDICT: endpoint treats different keys as distinct requests: 2 unique semantic responses

──────────────────────────────────

Result:
PASS (observed)

Concurrency trials: 1 observed
No divergent HTTP result was observed within the configured HTTP-visible checks.
It does not prove hidden downstream side effects were deduplicated.
```

Stop the demo:

```bash
docker compose -f examples/docker-compose.yml down
```

> **PowerShell:** prefer `--body-file` over inline `--body`; PowerShell
> mangles inline JSON quoting.

## What IdemCheck checks

| Check | Requests | PASSes when |
|---|---|---|
| Sequential retry ×2 | Same key, sent one after another | One logical result |
| Sequential retry ×10 | Same key, sent one after another | One logical result |
| Concurrent retry ×10 | Same key, released through a barrier, then settled and replayed | Every observed 2xx body is one logical result |
| Same key + changed payload | Same key, different body | The conflict is rejected (4xx) or the original result is replayed (classified, not assumed) |
| Different key + same payload | Two different keys, same body (control) | Endpoint treats different keys as distinct requests |

Each check uses its own derived idempotency key, so one scenario cannot
contaminate another. A failed baseline request (HTTP ≥ 400) errors the run
out with exit code `2` instead of guessing; evidence that cannot decide a
question is reported `INCONCLUSIVE` (exit `3`), never as a pass.

Responses are compared as **semantic fingerprints**, not raw bytes:

```text
HTTP response → parse → drop ignored JSON paths → canonical JSON
             → + status + filtered headers → SHA-256
```

Key order never matters. Noise headers (`date`, `x-request-id`,
`traceparent`, `x-amzn-trace-id`, `cf-ray`, `server-timing`) are ignored by
default. Non-JSON bodies fall back to normalized raw comparison; malformed
JSON never crashes the run.

## Verdicts, phases, policies

Every check ends in one of four verdicts, and the exit code always agrees
with the summary:

| Verdict | Meaning | Exit |
|---|---|---|
| `PASS` | Observations converged on one logical result (or the contract under test was met). | `0` |
| `FAIL` | Concrete divergence: distinct success bodies for one key, or two logical results under one key for two payloads. | `1` |
| `ERROR` | Execution failure: nothing usable was observed, or the baseline was rejected. | `2` |
| `INCONCLUSIVE` | Not enough evidence: unknown statuses, lost responses, oversize bodies, or transients never confirmed by replay. Never counted as a pass. | `3` |

The terminal prints a pass as `PASS (observed)`: convergence of the
configured checks above, with any active exclusions and the trial count
listed underneath — a screenshot of it does not assert universal
correctness. `summary.result` in JSON stays `PASS`.

A concurrent check shows how its verdict was reached:

```text
BURST: 10 requests released together (spread 0 ms)
SETTLE: 250ms before replay
REPLAY: 1 request -> HTTP 201 (confirmed a logical result)
VERDICT: 11 requests converged on 1 logical result (replay agreed)
```

- **BURST** — every worker waits on one barrier, then fires together.
- **SETTLE** — wait `--settle` (default `250ms`) before anything is
  replayed or judged. The wait is configuration, not an automatically
  discovered completion boundary: the default suits responsive endpoints,
  while APIs with long asynchronous processing may need a larger value.
- **REPLAY** — the same request with the same key after the settle
  window, bounded by `--replay-timeout` (default `5s`).
- **VERDICT** — the convergence decision, stated with its evidence.

Two policy profiles decide what counts as an acceptable transient:

- `safe-retry` (default) treats `409`, `429`, `503` as policy transients.
  A transient never passes on its own: it joins a `PASS` only when the
  replay answers with a logical result that matches the burst.
- `strict-replay` accepts no transients: in same-key checks any
  non-success response — including `409`/`429`/`503` — drives the verdict
  to `INCONCLUSIVE` rather than a pass.

Select with `--policy`; override the transient set with
`--transient-status 429,503`. Statuses outside the profile are never
guessed — they mean `INCONCLUSIVE`.

Repeat the burst with isolated per-trial keys to press on a race:

```bash
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --trials 3
```

```text
BURST: 10 requests × 3 trials (isolated per-trial keys)
SETTLE: 250ms × 3 trials
REPLAY: 3/3 confirmed a logical result
VERDICT: Concurrency race detected in 3 / 3 trials
```

A failing run reports `Concurrency race detected in N / M trials`; a
clean one reports `No observable race in N trials` — evidence, never a
proof that no race exists. The result block always states how many trials
were observed (`Concurrency trials: N observed`). Use multiple trials when
race windows are timing-sensitive; no particular count provides
mathematical proof. `--max-trials` (default `10`) bounds the run.

**Optional fault scenario.** Beyond the core checks above,
`--fault lost-response` wraps the target in a loopback-only proxy that
drops exactly one completed response (the request still runs upstream),
to watch a lost response become `INCONCLUSIVE` instead of a wrong
verdict:

```text
Sequential retry ×2 ....................... INCONCLUSIVE
  VERDICT: 1/2 requests failed before a verdict: Post "...": EOF
```

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

The burst is a **single-process, client-side concurrent burst**: one
process releases its own workers together. It does not simulate
cross-region retries, WAN jitter, multi-client clock differences, staggered
retry patterns, or load-balancer scheduling (see Limitations).

## Why not curl / k6 / hey / vegeta?

Those tools are excellent at sending traffic — sequential requests or
load. None of them asks the idempotency question: do duplicate requests
sharing one key still converge on one logical result?

IdemCheck adds the idempotency-specific behavior those tools do not model:

- **BURST → SETTLE → REPLAY → convergence verdict** — a barrier-released
  duplicate burst, a settle window, a same-key replay, then a stated
  verdict with its evidence.
- **Semantic response normalization** — key order, noise headers, and
  ignored fields never create or hide a divergence.
- **Transient-policy handling** — `safe-retry` / `strict-replay`
  profiles; unknown statuses become `INCONCLUSIVE`, never a guessed pass.
- **Payload/key contract checks** — same key with a changed payload, and
  a different key with the same payload as a control.
- **PASS / FAIL / INCONCLUSIVE with exact divergence evidence** — which
  fields differed, each fingerprint's values, a copy-pasteable re-run.
- **Safety and redaction** — loopback-only by default, credentials
  scrubbed from every output path, bounded concurrency.
- **CI exit codes** — `0`/`1`/`2`/`3` with a JSON document that always
  matches them.

A custom concurrent test script can cover the simple cases, and if yours
already does, keep it. IdemCheck exists to make the tricky parts
reusable: the convergence semantics, the safety boundaries, the evidence
format, the replay behavior, and a repeatable CI contract.

## CLI examples

```bash
# See every flag
idemcheck test --help
```

```text
IdemCheck sends sequential and concurrent duplicate requests sharing one
Idempotency-Key and reports whether the endpoint produces more than one
logical result — the signature of an idempotency race condition.

Every check reports PASS, FAIL, or INCONCLUSIVE (observations were
insufficient; never silently treated as a pass).

Exit codes: 0 pass, 1 violation, 2 config/execution error, 3 inconclusive.

Usage:
  idemcheck test [flags]

Flags:
      --allow-remote                   permit testing a non-local host
      --body string                    request body (inline)
      --body-file string               request body from file
      --concurrency int                concurrent request count (default 10)
      --config string                  YAML config file (response ignore lists)
      --fault string                   deterministic fault injection: none or lost-response (local proxy drops the first completed response) (default "none")
      --format string                  output format: text or json (default "text")
  -H, --header stringList              extra header "Name: value" (repeatable)
  -h, --help                           help for test
      --ignore-header stringArray      header name to ignore, repeatable (e.g. x-custom-trace)
      --ignore-json stringArray        JSON path to ignore, repeatable (e.g. $.request_id)
      --key string                     idempotency key (generated when omitted; set for deterministic repro)
      --key-header string              idempotency key header name (default "Idempotency-Key")
      --max-body-bytes int             per-response body read limit; larger bodies make the check inconclusive (default 4194304)
      --max-concurrency int            safety ceiling for --concurrency (default 50)
      --max-repeat int                 safety ceiling for --repeat (default 100)
      --max-trials int                 safety ceiling for --trials (default 10)
      --method string                  HTTP method (default "POST")
      --policy string                  verdict policy: safe-retry (default) or strict-replay (from config when empty)
      --repeat int                     sequential repeat count (default 10)
      --replay-timeout duration        retry budget for the replay phase (default 5s)
      --sensitive-header stringArray   extra header name to redact from all output (repeatable)
      --settle duration                wait after the burst before the replay phase (default 250ms)
      --timeout duration               per-request HTTP timeout (default 10s)
      --transient-status ints          HTTP statuses treated as acceptable transients, e.g. 409,429 (overrides --policy default and config)
      --trials int                     isolated concurrent bursts for race detection (each gets its own key) (default 1)
      --url string                     target URL (required)
  -v, --verbose                        show per-request timing detail
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

# Repeat the burst with isolated per-trial keys; report a race ratio
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --trials 3

# Strict evidence: no policy transients accepted
idemcheck test --url http://localhost:8081/orders \
  --body-file examples/request.json --policy strict-replay

# Optional fault scenario: lost response (deterministic, loopback-only)
idemcheck test --url http://localhost:8081/orders \
  --body-file examples/request.json --fault lost-response
```

Exit codes (precedence `1` > `2` > `3` > `0`):

| Code | Meaning |
|---|---|
| `0` | `PASS` — every check converged on one logical result |
| `1` | `FAIL` — a concrete divergence was observed |
| `2` | `ERROR` — configuration or execution problem |
| `3` | `INCONCLUSIVE` — insufficient evidence; never treated as a pass |

## JSON / CI usage

`--format json` emits a stable document with the same information as the
text report:

```bash
idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --format json > idemcheck.json
echo "exit=$?"   # 0 pass, 1 violation, 2 error, 3 inconclusive
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
  "checks_inconclusive": 0,
  "checks_skipped": 0,
  "policy": "safe-retry",
  "trials": 1
}
```

`violations` (trimmed from a real run):

```json
[
  {
    "check": "concurrent",
    "type": "concurrent_race",
    "message": "11 concurrent requests\n1 idempotency key\n10 distinct logical results",
    "differing_fields": [
      "$.order_id"
    ]
  }
]
```

Each `evidence` entry names one fingerprint group with its status and the
fields that differ; `reproduce.command` is a ready-to-paste re-run. Each
`checks[].phases` array records the `BURST` / `SETTLE` / `REPLAY` /
`VERDICT` lines, and `checks[].trials` records how many isolated bursts
produced the verdict. `summary.trials` records the requested trial count,
and `summary.ignore_json` / `summary.ignore_headers` list any active
exclusions — the same exclusions the terminal prints under the result.

`summary.result` is one of `PASS`, `FAILED`, `ERROR`, `INCONCLUSIVE`, and
always matches the exit code. In CI, treat exit `1` as a test failure, `2`
as a configuration problem, and `3` as "the run could not decide" — never
as a pass. The terminal's `PASS (observed)` label is presentation only;
the machine result stays `PASS`.

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

> **Ignore rules weaken the observation boundary.** Any ignored field can
> contain business identity depending on the API — a name that looks like
> a timestamp or a request ID is no guarantee that the field carries no
> business meaning, and IdemCheck does not guess which fields are safe.
> Ignoring a business identifier such as `$.order_id` hides the very
> difference an idempotency bug produces: running the unsafe demo with
> `--ignore-json '$.order_id'` turns the race check itself from `FAIL`
> into `PASS` while the different-keys control check degrades to
> `INCONCLUSIVE`, because responses for distinct keys then look identical —
> the run as a whole reports `INCONCLUSIVE` (exit `3`), not a pass. Every
> `PASS` result block lists the active exclusions verbatim; review them as
> carefully as the test itself.

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
  `--trials N` narrows the question ("No observable race in N trials"); it
  never proves the absence of a race.
- **One process, one client.** The synchronization barrier creates a
  single-process client-side concurrent burst. It does not reproduce
  cross-region retries, WAN jitter, multi-client clock differences, every
  staggered retry pattern, or every load-balancer scheduling pattern, and
  `--trials` repeats the observation without changing any of that.
- **Hidden side effects stay hidden.** Internal work that never changes an
  HTTP response body is invisible to any black-box checker, including this
  one (fixture J in [docs/TEST_MATRIX.md](docs/TEST_MATRIX.md)).
- **Structural detection.** Differing fields are found by structural JSON
  diff. A resource ID embedded in an unstructured text string cannot be
  named as a differing field; the fingerprint still differs and the check
  still fails, but the report shows fingerprint-level evidence only.
- **Ignore rules can hide failures.** See the warning above.

## Architecture

```text
cmd/idemcheck/        CLI entry point
internal/cli/         flag parsing + terminal rendering
internal/config/      options, safety guard, YAML config, policy profiles
internal/engine/      sequential + barrier runners, replay, trials, scenarios
internal/fingerprint/ normalization, ignore paths, JSON diff
internal/httpx/       request building, response reading, fault proxy
internal/models/      result schema (JSON contract)
internal/redact/      credential redaction for every output path
internal/report/      assemble + write text/JSON output
internal/buildinfo/   version from Go build info
examples/             safe + unsafe demo APIs, compose file, sample config
docs/                 executed test matrix, release design
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

`go test ./...` executes the reference fixtures A–J on every run; their
expected verdicts and the false-positive / false-negative contract live in
[docs/TEST_MATRIX.md](docs/TEST_MATRIX.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development loop and
[docs/RELEASE.md](docs/RELEASE.md) for the release design.

Issues and pull requests are welcome:
[bug report](https://github.com/hyukvoid/idemcheck/issues/new?template=bug_report.md) ·
[feature request](https://github.com/hyukvoid/idemcheck/issues/new?template=feature_request.md) ·
[security policy](SECURITY.md)

## License

[MIT](LICENSE)
