# Cora

Cora is a lightweight, agent-first error observability system for teams that
cannot justify the operational cost of Sentry or a full APM stack. It tails
existing application logs without changing business services, collapses error
floods into Problems, and applies explainable product-line decisions.

## Components

- **Cora Agent** runs once per application host, follows many Logback files,
  reconstructs multiline ERROR events, checkpoints acknowledged offsets, and
  sends bounded batches over the internal network.
- **Cora Server** receives events, fingerprints and aggregates them, persists
  Problems, trends, decisions, and immutable cases in SQLite, and exposes HTTP
  ingest/query APIs plus an Agent-facing Streamable HTTP MCP endpoint.
- **Cora Core** is the deterministic decision engine embedded in the Server.
  It loads versioned product-line experience from a **Cora Pack**.

The current production path is Agent-only. Java SDK and Logback Appender code
were intentionally removed because deploying application dependencies requires
business-development participation that is not currently available.

## Current capabilities

- Promtail-style YAML for both Server and multi-target Agent processes.
- Production Logback pattern parsing and multiline Java stacktraces.
- Per-target bounded pre-error breadcrumbs: trace-first, thread fallback.
- Upload-time redaction for credential keys, signed OSS/S3 URL query credentials,
  phone numbers, and identity numbers.
- Bearer-token protection for Server `/v1/*` APIs; `/healthz` remains public on
  the private listener.
- Durable atomic positions; rename and copy-truncate rotation handling.
- Retry on connection failures, HTTP 429, and HTTP 5xx; positions advance only
  after a 2xx response.
- Count- and byte-bounded events and batches.
- Deterministic Java fingerprints and bounded in-memory aggregation.
- Chronological first/latest samples even when historical events arrive
  newest-first.
- Service-scoped Problem identity plus cumulative and per-window node facts.
- SQLite schema migrations, Problems, trends, node trends, and Cora decisions.
- Product-line isolation and a versioned `gbjk-zhifu` Cora Pack with 131
  reviewed rules.
- Problem lifecycle states: `new`, `acknowledged`, `resolved`, and `recurring`;
  acknowledged-but-unhandled Problems remain in the current attention queue.
- Bearer-protected `/mcp` with `cora_list_attention`, `cora_get_problem`,
  `cora_record_outcome`, stable paginated `cora_export_cases`, and the read-only
  `cora_iteration_snapshot` and `cora_retention_audit`; outcome
  writes preserve an immutable context snapshot and exports freeze a high-water
  case ID for reproducible local snapshots.
- `cora_list_attention` groups Problems sharing representative trace IDs into
  one read-only incident view with a representative Problem, related Problem
  references, involved services, and shared traces; stored facts remain separate.
- Problem detail relates representative samples that share trace IDs across
  services, bounds MCP breadcrumbs, and redacts historical signed URL
  credentials at read time while preserving full stored and exported case
  snapshots.
- Server `/healthz`; Agent `/healthz` and `/readyz` in YAML mode.
- Low-volume operational logs cover process startup/shutdown, target file
  open/reopen, batch delivery/retry/failure, Server acceptance, and non-empty
  aggregation flushes without logging event bodies or credentials.
- Server `/readyz` verifies SQLite reachability and unrecovered write failure;
  Agent readiness reports per-target readability, worker state, delivery
  failure, acknowledged offset, lag, parse/truncation, retry, and send facts.
- Both binaries and health responses expose version, commit, build time, and Go
  version; Server storage status also exposes SQLite schema v5.
- Verified SQLite backup/check commands and a read-only `cora-canary` binary
  cover live HTTP plus MCP acceptance.
- A read-only `cora-iterate` workflow freezes case-export pages, summarizes one
  explicit product line and business date, flags anomalous high-frequency ignore
  rules, joins optional Atlas evidence, proposes rules only from repeated
  consistent handled cases, and writes an immutable shadow-evaluation bundle.
- A read-only `cora-retention-audit` command opens a consistent SQLite backup in
  `mode=ro&immutable=1`, verifies closure receipts and referenced artifact hashes, explains
  every Problem's retention blockers, and estimates logical—not physical—release.
- The MCP `cora_retention_audit` tool provides a live, product-scoped, read-only
  retention preflight with paged per-Problem blockers. It does not inspect local
  closure artifacts or authorize cleanup; the consistent-backup command remains
  mandatory before any retention mutation.

Not implemented yet: event-ID deduplication, alerts, case top-k retrieval in
Core, LLM gray-zone judgment, automatic candidate promotion, or a web UI. The
current Cora Core still loads an embedded static Pack at process start; the
offline workflow never activates its proposed candidates.

## Run locally

Start the Server:

```sh
go run ./cmd/cora-server \
  -config.file config/cora-server.example.yml \
  -check-config

go run ./cmd/cora-server \
  -config.file config/cora-server.example.yml
```

The example is production-shaped and expects `./auth.token`. For disposable
local development, the existing `-allow-unauthenticated -db ./cora.db` flags
remain available. Relative YAML paths use the process working directory.

Validate and start a multi-target Agent:

```sh
go run ./cmd/cora-agent \
  -config.file config/cora-agent-qikang.example.yml \
  -check-config

go run ./cmd/cora-agent \
  -config.file config/cora-agent-qikang.example.yml
```

The ingest and query APIs are:

- `POST /v1/events:batch`
- `GET /v1/problems?product_line=<line>`
- `GET /v1/attention?product_line=<line>`
- `GET /v1/trends?product_line=<line>&service=<service>&fingerprint=<fingerprint>`
- `GET /v1/node-occurrences?product_line=<line>&service=<service>&fingerprint=<fingerprint>`
- `GET /v1/node-trends?product_line=<line>&service=<service>&fingerprint=<fingerprint>[&node=<node>]`
- `POST /mcp` (Streamable HTTP MCP; use the same bearer token as `/v1/*`)
- `GET /healthz`

The MCP tools always require an explicit `product_line`; problem detail and
outcome writes additionally require `service` and `fingerprint`, so an Agent
cannot silently mix product-line cases. Case export starts with zero cursors,
then reuses the returned `snapshot_through_case_id` and `next_after_case_id`
until `has_more=false`; each page includes a SHA-256 over its canonical case
array for local persistence checks.

## Validate

```sh
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

After committing a release boundary, build identified Linux artifacts with:

```sh
deploy/scripts/build-release.sh v0.1.0-rc4
```

Run the reproducible Cora Pack shadow evaluation:

```sh
go run ./cmd/cora-eval \
  -input /path/to/cora-evaluation.csv \
  -product-line gbjk-zhifu \
  -json reports/cora-shadow-eval/cora-gbjk-v0-baseline.json \
  -markdown reports/cora-shadow-eval/cora-gbjk-v0-baseline.md
```

Run the read-only T+1 iteration workflow:

```sh
go run ./cmd/cora-iterate \
  -server-url https://cora.gbgoodness.com \
  -auth-token-file /path/to/auth.token \
  -product-line gbjk-zhifu \
  -business-date 2026-07-14
```

See `docs/RULE_ITERATION_WORKFLOW.md` for Atlas evidence input, deterministic
artifacts, frequency thresholds, and safety boundaries.

Audit a consistent SQLite backup without migration or cleanup:

```sh
go run ./cmd/cora-retention-audit \
  -db /secure/backups/cora-20260715T040000Z.db \
  -product-line gbjk-zhifu \
  -cora-build-version v0.1.0-rc6-dirty \
  -cora-source-digest 7d69633fd551a0ee6bdecbd167f9fe35412e1627a8729ef02ee5a2b376ba278e \
  -iteration-root out/iterations \
  -closure-root out/closure-receipts \
  -run-id gbjk-zhifu-20260715T040000Z
```

See `docs/RETENTION_AUDIT.md` for eligibility gates, stable reason codes,
artifact layout, logical-size estimation, and the production acceptance boundary.

## Documentation

- [`docs/README.md`](docs/README.md): documentation map and source-of-truth
  boundaries.
- [`docs/CORA_OVERVIEW.md`](docs/CORA_OVERVIEW.md): product and architecture
  overview for readers who are new to Cora.
- [`docs/HANDOFF.md`](docs/HANDOFF.md): current truth and next development loop.
- [`docs/CORA_AGENT_V0.md`](docs/CORA_AGENT_V0.md): Agent configuration,
  delivery semantics, and Supervisor deployment.
- [`docs/CORA_V0.md`](docs/CORA_V0.md): Core contract, Cora Pack, and evaluation
  limits.
- [`docs/VISION_ALIGNMENT.md`](docs/VISION_ALIGNMENT.md): original vision versus
  current truth, including the required MCP and case loop.
- [`docs/PRODUCTION_READINESS.md`](docs/PRODUCTION_READINESS.md): best-effort v0
  production contract, explicit non-goals, and canary gates.
- [`docs/RULE_ITERATION_WORKFLOW.md`](docs/RULE_ITERATION_WORKFLOW.md): read-only
  T+1 problem summary, evidence, candidate, and shadow-evaluation workflow.
- [`docs/RETENTION_AUDIT.md`](docs/RETENTION_AUDIT.md): B0 read-only retention
  audit, closure-receipt verification, and production-backup acceptance.
- [`docs/ADR_001_PRODUCTION_FACT_LIFECYCLE.md`](docs/ADR_001_PRODUCTION_FACT_LIFECYCLE.md):
  accepted lifecycle boundary between production hot facts and immutable local
  evidence.
- [`deploy/README.md`](deploy/README.md): Linux build, Supervisor canary,
  backup, and rollback.
- [`docs/PERFORMANCE_BASELINE.md`](docs/PERFORMANCE_BASELINE.md): reproducible
  aggregation benchmark.
