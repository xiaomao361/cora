# Cora Core v0 Contract and Guanbai Cora Pack

## Boundary

Cora Server owns deterministic event ingestion, fingerprints, aggregation,
storage, and query APIs. Cora Core receives one candidate Problem at flush time
and returns an explainable attention decision. The Go interface is deliberately
compatible with a future out-of-process Core:

```json
{
  "event": {},
  "fingerprint": "...",
  "occurrence_count": 3,
  "first_occurrence": false
}
```

The decision contract is:

```json
{
  "decision": "attention | observe | ignore",
  "category": "...",
  "rule_id": "...",
  "reason": "...",
  "source": "experience_pack | framework_default",
  "experience_version": "...",
  "decided_at": "..."
}
```

Every aggregate flush reevaluates the latest representative event with the
updated total occurrence count. The decision is stored transactionally with
the service-scoped Problem update in SQLite schema v4.

Cora Core is not allowed to block fact persistence. If the Core fails or
returns an invalid decision, Cora Server stores the Problem and records a
`framework_default` `observe` decision so the failure remains visible.

## Iteration boundary

The interface is replaceable, but the current implementation is not yet a
self-iterating Core: it embeds JSON experience Packs into the Server binary and
loads them once at process start. A rule change therefore still requires a new
binary release.

The original target is a concrete pipeline rather than generic online learning:
rules provide the stable fast path; an LLM judges gray-zone Problems with current
facts and retrieved product-line cases; Agent outcomes become new cases; repeated
consistent cases may be crystallized into a human-reviewed rule. A learned local
filter is gated until a product line has enough reviewed cases and a frozen eval
set.

The repository has evaluation and feedback schemas, but no case persistence,
retrieval, LLM adapter, candidate-rule promotion, hot reload, or learned filter.
Static Pack reload alone would improve operations but would not fulfill the Core
iteration loop. Automatic production activation without evaluation and rollback
is outside the current direction.

## Guanbai experience baseline

The embedded `gbjk-zhifu` pack is the first versioned product-line Cora Pack
experience baseline. It contains 130 reviewed rules:

- 27 `attention`
- 41 `observe`
- 62 `ignore`

The original priority is preserved: `attention`, then `observe`, then `ignore`.
Rules match stable logger/class, stack method, message, and exception fragments.
No raw production logs, labeled CSV rows, legacy model weights, credentials, or
source-system reports are copied into Cora.

This is explicitly a product-line experience pack. It runs only when
`product_line=gbjk-zhifu`. An error from another product line never matches
these rules and receives the conservative framework default `observe`.

Unmatched Guanbai events also resolve to `observe`; Cora v0 does not silently
turn an unknown signature into `ignore`.

## Query semantics

`GET /v1/attention?product_line=<line>` returns `attention` and `observe`
Problems, ordered with `attention` first. `ignore` decisions remain persisted
but are omitted from this attention queue. `GET /v1/problems` continues to
return all deterministic Problems regardless of Cora's decision.

## Shadow evaluation

Golden tests cover a database-disconnect attention rule, a client-token ignore
rule, unmatched Guanbai fallback, and cross-product-line isolation. A real HTTP
run ingested those shapes, flushed SQLite, and returned the expected attention
queue.

`cmd/cora-eval` performs a read-only, reproducible evaluation against a
Cora evaluation CSV and writes aggregate JSON plus a redacted Markdown report.
The baseline source SHA-256 is stored in the report so a later export cannot be
mistaken for the same dataset.

The current 1,404-row baseline found:

- 31 attention, 559 observe, and 814 ignore decisions.
- 60.2% decisive coverage and 99.3% agreement among decisive rows.
- 25/45 old attention rows remain attention; the other 20 move to explicit
  observe under `at_05`. No old attention row becomes ignore.
- Six historical default-ignore rows move to attention under `at_06`. Review
  confirmed they were unmatched rather than explicitly ignored, so Cora keeps
  the core-claim-chain failure visible.
- 98.6% of rows repeat one of only 20 approximated Cora fingerprints.
- No full timestamps are parseable, strict CSV parsing fails on one bare quote,
  and exception/stack is missing on 1,403/1,404 rows.

Therefore the experience pack is suitable as a conservative, explainable rule
layer, but this dataset is not trustworthy for time-split model evaluation or
production-fingerprint accuracy. Historical statistical weights remain
intentionally unloaded. A fresh export must preserve full timestamps,
exception type, stacktrace, stable source/service, and reviewed three-way labels
before statistical model adoption is reconsidered.

## Version and data gates

`config/cora-base-v0.json` is the deployable Core manifest. It binds the Cora
contract, experience-pack version, product line, evaluation evidence, and
fail-open behavior. `schemas/cora-evaluation-row.v1.schema.json` and
`schemas/cora-feedback.v1.schema.json` define the stable records that Cora Agent
and a later review workflow may emit.

Cora remains in deterministic rule mode until a new dataset has at least 300
independently reviewed fingerprints, including at least 50 attention, 50
ignore, and 100 observe outcomes; full timestamps, exception types, stacktraces,
service, and source must each reach 95% coverage. Evaluation must split by time
and group by fingerprint. Until then, model-quality claims are blocked.
