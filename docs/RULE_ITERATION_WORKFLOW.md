# Cora T+1 rule iteration workflow

`cora-iterate` implements target A as a local, read-only workflow. It reads one explicit
production Cora product line, freezes an immutable case snapshot, captures the selected
business day's read-only iteration snapshot, joins separately collected Atlas evidence,
and produces proposed rules plus a shadow evaluation. It never calls
`cora_record_outcome`, changes a Pack, or writes production SQLite.

## Inputs and boundaries

Required inputs:

- `server-url` and a bearer token file for the Cora HTTP/MCP endpoint;
- one explicit `product-line`;
- one `business-date` interpreted in an explicit IANA timezone;
- the reviewed local Pack manifest used to identify the expected Pack version and SHA-256.

The default timezone is `Asia/Shanghai`. When `business-date` is omitted, the command selects
the previous calendar day in that timezone. For a scheduled or audited run, pass the date
explicitly.

Cora provides runtime facts: all decisions (including `ignore`), business-day and baseline
counts, node distribution, related Problems, immutable cases, and Server build/schema identity.
The runner reads the day boundary through `cora_iteration_snapshot`; it does not infer the day
from the current `/v1/problems` list. Atlas evidence is collected
separately because Atlas and Cora have different source-of-truth boundaries. Query Atlas by
calling `atlas_systems()` first and passing `system="gbjk-zhifu"` on every subsequent query.
Do not query or merge another product line by default.

## Atlas evidence file

Code, dependency, and release findings can be supplied as JSON Lines with one record per
service-scoped fingerprint:

```json
{"schema_version":"cora.code-evidence.v1","evidence_id":"atlas-gb-order-fp-001","product_line":"gbjk-zhifu","service":"gb-order","fingerprint":"0123456789abcdef0123456789abcdef","source":"atlas","status":"verified","summary":"gb-order owns the callback boundary and the current release did not change it","references":["atlas_service_profile:gb-order","atlas_releases:2026-07-14"],"collected_at":"2026-07-15T01:00:00Z"}
```

Allowed status values are `verified` and `not_found`. `not_found` is evidence of a query with
no supporting fact, not permission to invent a conclusion. The runner rejects a record from
another product line. It copies canonicalized evidence into the immutable run directory and
records `not_collected` in triage when no matching record was supplied.

## Run command

```sh
go run ./cmd/cora-iterate \
  -server-url https://cora.gbgoodness.com \
  -auth-token-file /secure/path/auth.token \
  -product-line gbjk-zhifu \
  -business-date 2026-07-14 \
  -timezone Asia/Shanghai \
  -code-evidence /secure/path/atlas-evidence.jsonl \
  -output-root out/iterations
```

Defaults used for frequency review:

- compare the selected day with the preceding seven complete business days;
- require at least 20 occurrences;
- flag an ignore rule when the selected day is at least three times the prior daily average,
  or when the prior average is zero and the minimum count is exceeded.

These are review thresholds, not decision rules. Override them with `-baseline-days`,
`-frequency-minimum`, and `-frequency-ratio`; the generated report preserves the measured
counts and baseline.

## Immutable output

The final directory is:

```text
out/iterations/<product_line>/<business_date>/<iteration_run_id>/
  run.json
  case-snapshot.jsonl
  case-snapshot-manifest.json
  iteration-snapshot.json
  attention-incidents.json
  triage-results.jsonl
  code-evidence.jsonl          # only when evidence was supplied
  rule-candidates.json
  candidate-pack.patch         # only when at least one candidate passed the gates
  shadow-eval.json
  shadow-eval.md
```

Each artifact is SHA-256-addressed from `run.json`. `iteration-snapshot.json` preserves the
exact read-only MCP input used for the summary. The runner verifies every MCP case page
against the Server-provided hash and keeps the first page's high-water ID for every later
page. It writes into a temporary directory and publishes with one rename only after all
steps succeed. An incomplete or hash-mismatched run never appears as `completed`.

`candidate-pack.patch` is an RFC 6902-style JSON patch for human review; the workflow does
not apply it.

The CLI reads the bearer token from the local file named by `-auth-token-file`. A Codex MCP
connector may manage authentication separately; that connector credential is not automatically
available to the local `go run` process.

## Candidate gates

A candidate is generated only when all of the following are true:

1. at least two immutable cases share the same `product_line + service + fingerprint`;
2. every case is handled and has a consistent real-problem/noise outcome;
3. their latest samples agree on the narrow Pack-compatible class/method/exception matcher;
4. the proposed decision differs from the frozen baseline decision;
5. matching Atlas evidence has `status=verified`.

The result remains `status=proposed`. High-frequency ignore findings do not bypass these
gates; they remain `ignore_frequency_escalation` triage entries until business meaning and
code/release evidence have been reviewed.

## Shadow evaluation

The report applies candidates only in memory to:

- Problems with occurrences in the selected business window;
- handled cases in the frozen snapshot.

It reports Problem and occurrence transitions, known real-problem recall, known noise moved
to attention, candidate matches, and frequency escalations. With no gated candidate, the
report is still a valid baseline summary and all transitions remain unchanged.

Pack promotion, deployment, and 2h/24h/72h observation are separate, explicitly reviewed
steps. Only those later steps can produce a closure receipt; this workflow alone never makes
production facts retention-eligible.

The first production-confirmed noise refinement is `cora-gbjk-v0.1.1`: the exact breadcrumb
`[特殊人群补助]未在投保信息中找到` is a normal business decision, not a Redisson or Seata failure.
The rule uses breadcrumb include/exclude matching so the narrow ignore does not weaken the generic
Redisson/Seata attention rules. The legacy 1,404-row CSV cannot shadow this field because it did not
retain breadcrumbs; live immutable Cases and golden regressions are the acceptance evidence until a
fresh full-fidelity evaluation export exists.
