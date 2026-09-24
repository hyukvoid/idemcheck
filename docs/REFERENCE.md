# IdemCheck reference

This guide covers the checks, policies, options, safety rules, and output
format in IdemCheck v1.0.0.

## Checks performed

A run performs five checks:

| Check | Requests | PASSes when |
|---|---|---|
| Sequential retry ×2 | Same key, one after another | One logical result |
| Sequential retry ×10 | Same key, one after another | One logical result |
| Concurrent retry ×10 | Same key, barrier-released, settled, replayed | Every observed 2xx body is one logical result |
| Same key + changed payload | Same key, different body | The conflict is rejected (4xx) or the original result is replayed |
| Different key + same payload | Two keys, same body (control) | The endpoint treats the keys as distinct |

Each check derives its own idempotency key from the base key, so scenarios
cannot contaminate one another. A rejected baseline (HTTP ≥ 400) errors the
run with exit `2`. When evidence cannot decide a question, the verdict is
`INCONCLUSIVE` (exit `3`), never a pass.

## Response fingerprints

IdemCheck compares semantic fingerprints, not raw response bytes:

```text
HTTP response → parse → drop ignored JSON paths → canonical JSON
             → + status + filtered headers → SHA-256
```

JSON key order does not matter. By default, IdemCheck ignores the headers
`date`, `x-request-id`, `traceparent`, `x-amzn-trace-id`, `cf-ray`, and
`server-timing`. Non-JSON bodies use normalized raw comparison. Malformed
JSON does not crash the run.

## Policies and transient responses

`safe-retry` is the default policy. It treats HTTP `409`, `429`, and `503` as
policy transients. A transient counts toward a pass only when replay returns
a logical result that matches the burst.

`strict-replay` accepts no transients. A non-success response in a same-key
check makes that check `INCONCLUSIVE` instead of passing.

Choose a policy with `--policy`. Override the transient set with
`--transient-status 429,503`. A status that the active policy does not accept
is not guessed; it leaves the check `INCONCLUSIVE`.

## Concurrency trials

Use `--trials` when the race depends on timing. Each trial gets an isolated
key:

```bash
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --trials 3
```

The report summarizes a three-trial run like this:

```text
  BURST: 10 requests × 3 trials (isolated per-trial keys)
  SETTLE: 250ms × 3 trials
  REPLAY: 3/3 confirmed a logical result
  VERDICT: Concurrency race detected in 3 / 3 trials
```

A failing run reports `Concurrency race detected in N / M trials`. A run
without an observed race reports `No observable race in N trials`. Both
describe the trials that ran; no trial count proves a race is absent. The
result block states `Concurrency trials: N observed`. `--max-trials` defaults
to 10 and bounds the number of trials.

## Lost-response fault scenario

`--fault lost-response` runs the target through a loopback-only proxy. The
proxy drops exactly one completed response after the request has run
upstream. This lets you observe how a lost response affects the verdict:

```text
Sequential retry ×2 ....................... INCONCLUSIVE
  VERDICT: 1/2 requests failed before a verdict: Post "...": EOF
```

The lost response is a fault in the client-visible exchange; it does not
cancel the upstream operation.

## Common usage recipes

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

# Heavier burst (capped by --max-concurrency)
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --concurrency 25 --repeat 20

# Three isolated trials; report a race ratio
idemcheck test --url http://localhost:8082/orders \
  --body-file examples/request.json --trials 3

# Strict evidence: no policy transients accepted
idemcheck test --url http://localhost:8081/orders \
  --body-file examples/request.json --policy strict-replay
```

For PowerShell, use `--body-file` when passing JSON; inline `--body` quoting
can be mangled by PowerShell.

## Common flags

| Flag | Default | Purpose |
|---|---|---|
| `--url` | required | target URL |
| `--method` | `POST` | HTTP method |
| `--body` / `--body-file` | (none) | request body, inline or from file |
| `--key` / `--key-header` | generated / `Idempotency-Key` | idempotency key and header name |
| `--concurrency` | `10` | burst size; ceiling `--max-concurrency` (default `50`) |
| `--repeat` | `10` | sequential repeats; ceiling `--max-repeat` (default `100`) |
| `--trials` / `--max-trials` | `1` / `10` | isolated bursts, each with its own key |
| `--settle` | `250ms` | wait between burst and replay |
| `--replay-timeout` | `5s` | replay budget |
| `--timeout` | `10s` | per-request HTTP timeout |
| `--max-body-bytes` | `4194304` | response read limit; larger bodies go `INCONCLUSIVE` |
| `--policy` | `safe-retry` | `safe-retry` or `strict-replay` |
| `--transient-status` | profile set | override acceptable transients |
| `--format` | `text` | `text` or `json` |
| `--ignore-json` / `--ignore-header` | (none) | response exclusions (see Ignore rules) |
| `--allow-remote` | off | permit a non-local target |
| `--fault` | `none` | `lost-response` fault scenario |
| `--verbose` | off | per-request timing detail |
| `--config` | (none) | YAML config file |

`idemcheck test --help` prints every flag with its full description.

## JSON and CI

Write a JSON report to a file and preserve the command's exit status:

```bash
idemcheck test \
  --url http://localhost:8082/orders \
  --body-file examples/request.json \
  --format json > idemcheck.json
echo "exit=$?"   # 0 pass, 1 violation, 2 error, 3 inconclusive
```

A shortened `summary` from a run looks like this:

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

The document also carries `checks`, `violations` (including
`differing_fields`), `evidence` fingerprint groups, a ready-to-paste
`reproduce.command`, per-check phases (`BURST` / `SETTLE` / `REPLAY` /
`VERDICT`), `trials`, and the active `ignore_json` / `ignore_headers` rules.

In CI, treat exit `1` as a test failure, `2` as a configuration problem, and
`3` as a run that could not decide. Do not treat `3` as a pass. In JSON,
`summary.result` is `PASS`, `FAILED`, `ERROR`, or `INCONCLUSIVE` and matches
the exit code. The terminal displays the passing result as `PASS (observed)`.
When multiple verdicts apply, precedence is `1 > 2 > 3 > 0`.

## Safety

### Remote guard

IdemCheck deliberately sends duplicate POST requests. It refuses non-local
targets unless you pass `--allow-remote`:

```console
$ idemcheck test --url http://api.example.invalid/orders --body-file examples/request.json
Error: refusing to test non-local target "api.example.invalid": this tool intentionally sends duplicate POST requests.
Re-run with --allow-remote if api.example.invalid is a test/staging environment you own
```

The refusal exits `2` before sending a request. `localhost`, `127.0.0.0/8`,
and `::1` are allowed. With `--allow-remote`, IdemCheck proceeds after a
warning; use it only on environments you're authorized to test. The default
ceilings are `--max-concurrency 50` and `--max-repeat 100`. Credentials are
redacted from output. Add header names with `--sensitive-header`.

### Ignore rules

Ignore volatile response fields before fingerprinting:

```yaml
# examples/idemcheck.yaml
response:
  ignore_json:
    - $.request_id
    - $.timestamp
  ignore_headers:
    - x-custom-trace
```

JSON paths use JSONPath-style prefixes such as `$.request_id`,
`$.meta.trace_id`, and `$.items[*].ts`. Matching object keys are deleted.
Matching array elements become `null` so the array shape stays comparable.
You can set the same rules with `--ignore-json '$.request_id'` and
`--ignore-header x-custom-trace`.

Ignoring a field weakens the check. No field name is inherently safe. An
ignored field can carry business identity, and IdemCheck does not guess which
fields matter. For example, ignoring `$.order_id` on the unsafe demo changes
the race check from `FAIL` to `PASS`. The different-key control then becomes
`INCONCLUSIVE` because the distinct keys look identical, so the whole run is
`INCONCLUSIVE` (exit `3`), not a pass. Every pass result lists the active
exclusions; review them along with the test.

## Limitations

- **HTTP-visible scope.** IdemCheck observes responses. It does not inspect
  database rows, queue messages, emails, payments, or other hidden effects.
  Fixture J in the [test matrix](TEST_MATRIX.md) covers this boundary.
- **A pass is evidence from configured runs.** One semantic response in the
  observed trials does not rule out every possible interleaving.
  `--trials N` narrows the observation but cannot prove a race is absent.
- **One process and client.** The barrier releases workers in one process.
  It does not reproduce cross-region retries, WAN jitter, multi-client clock
  differences, every staggered retry, or every load-balancer schedule.
  `--trials` repeats the same observation without changing that model.
- **Structural field reporting.** The JSON diff names differing fields. A
  resource ID inside an unstructured text string still changes the
  fingerprint and fails the check, but the report shows fingerprint-level
  evidence instead of a field name.
- **Ignore rules can hide failures.** See the warning above.

## Architecture and package layout

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

Workers prepare first, then release together. Results land in per-worker
slots, so the hot path takes no locks. Each scenario derives its own key from
the base key to keep checks isolated.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for contribution guidance, [RELEASE.md](RELEASE.md) for release steps, and the [README](../README.md) for the project overview.
