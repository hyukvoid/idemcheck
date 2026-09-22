# IdemCheck

**Break your idempotency implementation before production does.**

IdemCheck is a CLI that tests whether an `Idempotency-Key` implementation
actually survives retries and concurrency. A backend can look perfectly
idempotent under sequential requests and still create duplicate resources the
moment the same key arrives concurrently. IdemCheck reproduces that race
automatically.

## The 30-second demo

```bash
docker compose -f examples/docker-compose.yml up -d
```

Two `POST /orders` APIs accept the same payload and the same
`Idempotency-Key`. One handles the key atomically; the other checks the key,
sleeps, inserts the order, then saves the key — with no synchronization.

**Safe API — passes:**

```bash
idemcheck test \
  --url http://localhost:8081/orders \
  --body '{"item_id":42,"qty":1}'
```

```text
Sequential retry ×2 ....................... PASS
Sequential retry ×10 ...................... PASS
Concurrent retry ×10 ...................... PASS
Same key + changed payload ................ PASS
Different key + same payload .............. PASS

Result:
PASS
```

**Unsafe API — fails under concurrency:**

```bash
idemcheck test \
  --url http://localhost:8082/orders \
  --body '{"item_id":42,"qty":1}'
```

```text
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

Requests:
10

Unique semantic responses:
10

Fingerprint A ×1
  status: 201
  $.order_id: 803

Fingerprint B ×1
  status: 201
  $.order_id: 807

...

Differing fields:
  $.order_id

Re-run:

idemcheck test \
  --url http://localhost:8082/orders \
  --body '{"item_id":42,"qty":1}' \
  --key idemcheck-4f2a91c0b7d3

──────────────────────────────────

Result:
FAILED
```

Sequential retries pass because the replay path is correct — only the
concurrent burst exposes the check-then-insert race. Exit code is `1`.

## Install

```bash
go install github.com/hyukvoid/idemcheck/cmd/idemcheck@latest
```

Or build from source:

```bash
git clone https://github.com/hyukvoid/idemcheck
cd idemcheck
go build ./...
go build -o bin/idemcheck ./cmd/idemcheck
```

## Usage

```bash
idemcheck test --url http://localhost:8080/orders --body '{"item_id":42}'
```

```text
idemcheck test [flags]

  --url string             target URL (required)
  --method string          HTTP method (default POST)
  --body string            request body (inline)
  --body-file string       request body from file
  -H, --header strings     extra header "Name: value" (repeatable)
  --key-header string      idempotency key header (default Idempotency-Key)
  --key string             idempotency key (generated when omitted; set for
                           deterministic repro)
  --concurrency int        concurrent request count (default 10)
  --repeat int             sequential repeat count (default 10)
  --format string          output format: text or json (default text)
  --allow-remote           permit testing a non-local host
  --timeout duration       per-request HTTP timeout (default 10s)
  --config string          YAML config file (response ignore lists)
  --ignore-json strings    JSON path to ignore, repeatable (e.g. $.request_id)
  --ignore-header strings  header to ignore, repeatable (e.g. x-custom-trace)
  --max-concurrency int    safety ceiling for --concurrency (default 50)
  --max-repeat int         safety ceiling for --repeat (default 100)
  -v, --verbose            show per-request timing detail
```

Full example with auth:

```bash
idemcheck test \
  --url http://localhost:8080/orders \
  -H "Authorization: Bearer test-token" \
  --body-file examples/request.json \
  --concurrency 20
```

> **PowerShell tip:** prefer `--body-file` over inline `--body`. PowerShell
> rewrites quotes inside inline JSON arguments.

## What it checks

| Check | Requests | Passes when |
|---|---|---|
| Sequential retry ×2 | same key ×2 | one semantic response |
| Sequential retry ×10 | same key ×10 | one semantic response |
| Concurrent retry ×10 | same key ×10, barrier-released | one semantic response |
| Same key + changed payload | same key, two payloads | conflict is rejected or original replayed (classified, not assumed) |
| Different key + same payload | two keys (control) | endpoint distinguishes keys |

### The concurrency engine

The concurrent check is not a loop of `go send()`. Workers are spawned first,
each signals READY and parks on a channel; only when **all** workers are
ready does the main goroutine close the start channel, releasing every
request at the same instant. Each worker writes to its own pre-allocated
slot, so collection is lock-free and race-detector clean.

`--verbose` prints per-request start offsets and latencies so you can verify
the burst really was simultaneous:

```text
Timings — Concurrent retry ×10 (ms after barrier release | latency):
  worker  0: +  0 | 254
  worker  1: +  0 | 254
  ...
  spread: all requests initiated within 0 ms of barrier release
```

### Semantic fingerprints

Real responses differ byte-for-byte for reasons that don't matter:
timestamps, `request_id`, `trace_id`, `Date`, load-balancer headers. Raw
comparison would drown the signal in noise, so IdemCheck fingerprints a
normalized form instead:

```
HTTP response → parse → drop ignored JSON paths → canonical JSON
             → (key order never matters) + status + filtered headers
             → SHA-256
```

Ignore volatile fields per project:

```yaml
# idemcheck.yaml
response:
  ignore_json:
    - $.request_id
    - $.timestamp
    - $.meta.trace_id
  ignore_headers:
    - date
    - x-request-id
    - traceparent
```

```bash
idemcheck test --config idemcheck.yaml --url ... --body-file ...
```

`date`, `x-request-id`, `traceparent`, `x-amzn-trace-id`, `cf-ray`, and
`server-timing` are ignored out of the box. Non-JSON bodies fall back to
normalized raw comparison; malformed JSON never crashes the run.

### Evidence, not guessing

IdemCheck is black-box HTTP testing. It reports what it observed:

- `3 unique semantic responses observed`
- differing JSON paths (`$.order_id`) with each fingerprint's observed values

It does **not** claim anything about database rows, queue messages, emails,
or any other downstream side effect. Identical HTTP responses are strong
evidence, not proof, that everything behind the endpoint was deduplicated.
Verify critical side effects with your own observability.

## Automation

```bash
idemcheck test --url http://localhost:8080/orders --body-file req.json --format json
```

```json
{
  "tool": "IdemCheck",
  "version": "0.1.3",
  "target": { "url": "...", "method": "POST", "key_header": "Idempotency-Key", "key": "..." },
  "summary": { "result": "FAILED", "exit_code": 1, "checks_passed": 4, "checks_failed": 1 },
  "checks": [ { "id": "concurrent", "status": "fail", "requests": 10, "unique_responses": 10, "...": "..." } ],
  "violations": [ { "check": "concurrent", "type": "concurrent_race", "differing_fields": ["$.order_id"] } ],
  "evidence": [ { "check": "concurrent", "label": "A", "count": 1, "status": 201, "fields": { "$.order_id": 803 } } ]
}
```

### Exit codes

| Code | Meaning |
|---|---|
| `0` | tests passed (warnings allowed) |
| `1` | idempotency violation detected |
| `2` | invalid configuration or execution failure |

Warnings (`WARN` checks, e.g. a key accepted for different payloads) don't
fail the run; violations do.

## Safety

This tool **intentionally sends duplicate POST requests.** By default it
refuses non-local targets:

```text
Error: refusing to test non-local target "api.example.com": this tool
intentionally sends duplicate POST requests.
Re-run with --allow-remote if api.example.com is a test/staging environment you own
```

`localhost`, `127.0.0.0/8`, and `::1` work immediately. Request volume is
capped (`--max-concurrency` 50, `--max-repeat` 100) so a fat-fingered flag
can't turn into a load test.

## Examples

```text
examples/
├── safe-order-api/     per-key mutex: first writer creates, others replay
├── unsafe-order-api/   check → sleep → insert → save key (TOCTOU race)
├── request.json        sample POST body
├── idemcheck.yaml      sample ignore config
└── docker-compose.yml
```

Both are small enough to read side by side — the difference between them is
the entire point of this project.

## Development

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Limitations

- Black-box HTTP only. Side effects that never reach the response body
  (database rows, Kafka messages, emails) are out of scope — see above.
- A pass means: across N sequential retries and N barrier-released concurrent
  duplicates, the endpoint returned one semantic response. It is not a
  mathematical proof of idempotency under every interleaving.
- Resource-ID detection is structural JSON diffing; APIs that bury IDs in
  unstructured strings get fingerprint-level evidence only.

## License

MIT — see [LICENSE](LICENSE).
