# Test matrix: reference fixtures A–J

This document is the false-positive / false-negative contract of IdemCheck.
It is executed on every `go test ./...` run by
`internal/engine/fixtures_test.go` (`TestReferenceFixturesMatrix`): if the
code and this table ever disagree, the tests fail.

## What a verdict means

| Verdict | Meaning | Exit code |
| --- | --- | --- |
| PASS | Every observed response converged on one logical result (or the check's contract was met, e.g. a payload conflict was rejected). | 0 |
| FAIL | A concrete divergence: two distinct success bodies for one key, or one key yielding two logical results for two payloads. | 1 |
| ERROR | Execution failure: nothing usable was observed, or the control request was rejected. | 2 |
| INCONCLUSIVE | Observations were insufficient: unknown statuses, oversize bodies, transport failures, or transients never confirmed by a replay. | 3 |

Precedence: FAIL (1) > ERROR (2) > INCONCLUSIVE (3) > PASS (0).
Uncertainty never becomes a PASS and never aggregates into a PASS.

**Verdict identity is the normalized 2xx response body.** Status class
(201 vs 200) and headers collapse — they remain evidence, not identity.

## What the tool observes (the black-box boundary)

IdemCheck sends HTTP requests and reads HTTP responses. Nothing else.
It does not inspect databases, queues, traces, or adapter internals, and it
makes no claim about work an endpoint performs that never changes a response
body. The contract it tests is: *common Idempotency-Key contracts and retry
behaviors as observable through responses.*

## The fixtures

Each fixture is a local `httptest` endpoint. Every fixture runs the full
check matrix (sequential ×2, sequential ×N, concurrent burst + settle +
replay, payload conflict, distinct-key control).

| # | Behavior | seq2 | seqN | concurrent | payload | distinct | Exit |
| --- | --- | --- | --- | --- | --- | --- | --- |
| A | Correct idempotent store: one stored result per key, 409 on payload change | PASS | PASS | PASS | PASS | PASS | 0 |
| B | Status class shifts: 201 first answer, 200 replays, identical body | PASS | PASS | PASS | PASS | PASS | 0 |
| C | Non-idempotent race: check-then-act, no lock (TOCTOU window) | PASS | PASS | **FAIL** | PASS | PASS | 1 |
| D | Volatile envelope: `request_id`/`timestamp`/`trace_id` regenerate per response | PASS | PASS | PASS | PASS | PASS | 0 |
| E | Payload conflict accepted: dedup keyed without the payload | PASS | PASS | PASS | **FAIL** | PASS | 1 |
| F | Constant responder: byte-identical answer for every key and payload | PASS | PASS | PASS | PASS | INCONCLUSIVE | 3 |
| G | Rate-limited duplicate: 429 on the second request per key | PASS | PASS | PASS | INCONCLUSIVE | PASS | 3 |
| H | Unknown status: 500 on the second request per key | INCONCLUSIVE | INCONCLUSIVE | INCONCLUSIVE | INCONCLUSIVE | PASS | 3 |
| I | Lost response: local proxy drops the first completed answer | INCONCLUSIVE | PASS | PASS | PASS | PASS | 3 |
| J | Hidden side effect: identical responses, extra internal work per request | PASS | PASS | PASS | PASS | PASS | 0 |

## False-positive matrix (what must never be reported wrongly)

| Risk | Fixture | Required behavior | Why it holds |
| --- | --- | --- | --- |
| 201 vs 200 with the same body reported as a race | B | PASS | Verdict identity is the normalized body; status class collapses |
| Volatile per-response fields reported as a race | D | PASS | Default volatile keys (`timestamp`, `request_id`, `trace_id`, `correlationid`, `spanid`) are normalized away at any depth |
| A correct endpoint failed because a duplicate was rate limited | G | PASS, via replay confirmation | 429 is a policy transient; PASS only after the replay answers with a logical result |
| Verdict flips with arrival order (a transient observed first skips the replay) | G | PASS for any burst arrival order | The replay triggers on policy transients regardless of which response is observed first; only non-policy rejections block it |
| A 500 reported as a race | H | INCONCLUSIVE | Unknown statuses are never classified as divergence |
| An unobserved (lost) response counted as convergence | I | INCONCLUSIVE | Transport failures block the verdict; the retry with the same key is still observed |
| An unobserved (lost) response reported as a failure | I | INCONCLUSIVE, not FAIL | Only two distinct success bodies prove divergence |
| A race hidden by sequential success reported as PASS | C | FAIL | Barrier-synchronized burst overlaps the TOCTOU window |
| One key, two payloads, two results reported as PASS | E | FAIL | Payload check compares logical results under one key |
| Control check reported PASS when keys are indistinguishable | F | INCONCLUSIVE | Identical responses make the control uninformative |
| Uncertainty aggregating into a PASS across trials | trials | FAIL > ERROR > INCONCLUSIVE > PASS | Aggregate takes the most decisive rank; a single inconclusive trial blocks PASS |
| Credentials echoed into reports, evidence, or repro commands | redaction tests | redacted | Sensitive headers, URL userinfo, and query parameters are scrubbed on every output path |

## False-negative matrix (known limits, stated honestly)

| Limitation | Fixture | Statement |
| --- | --- | --- |
| Hidden side effects | J | Internal work that never changes a response body is invisible to a black-box checker. IdemCheck does not detect hidden side effects and never claims to. |
| Races that never overlap in time | C | A defect that only manifests with tighter timing than the burst can produce stays invisible. |
| Untested payloads and keys | E | Only the payload pair the tool sends is covered. |
| Uninformative responses | F | When every answer is byte-identical, no response-level race can be observed. |
| Unclassified statuses | H | A status outside the policy ends the verdict at INCONCLUSIVE rather than a guess. |
| The lost response itself | I | What happened to the dropped response is unknowable; only the retry with the same key is observed. |
| Volatile keys that are business fields | D | Normalization ignores keys by name; if a business field is named like a trace id, it is ignored too. |
| Rate-limited originals | G | When the original request is rate limited, the payload contract is not evaluable. |

## Reproducing

```
go test ./internal/engine/ -run TestReferenceFixturesMatrix -count=1
go test ./internal/engine/ -run 'ReferenceFixtures|FixtureJ' -count=10
go test ./...
go test -race ./...
```

The full matrix runs as part of `go test ./...`. Repetition counts used for
release validation are recorded in the release notes of the corresponding
version.

## Out of scope by design

OpenAPI scanning, database or queue adapters, OpenTelemetry, Kafka,
webhooks, dashboards/UI, AI-generated assertions, load testing, and generic
chaos platforms are not part of IdemCheck and are not simulated by any
fixture. The only injected fault is the deterministic `--fault
lost-response` proxy described by fixture I.
