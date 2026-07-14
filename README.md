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
  Problems, trends, and decisions in SQLite, and exposes HTTP query APIs.
- **Cora Core** is the deterministic decision engine embedded in the Server.
  It loads versioned product-line experience from a **Cora Pack**.

The current production path is Agent-only. Java SDK and Logback Appender code
were intentionally removed because deploying application dependencies requires
business-development participation that is not currently available.

## Current capabilities

- Promtail-style YAML with multiple explicit log targets per host.
- Production Logback pattern parsing and multiline Java stacktraces.
- Per-target bounded pre-error breadcrumbs: trace-first, thread fallback.
- Upload-time redaction for credential keys, phone numbers, and identity numbers.
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
- Product-line isolation and a versioned `gbjk-zhifu` Cora Pack with 130
  reviewed rules.
- Server `/healthz`; Agent `/healthz` and `/readyz` in YAML mode.

Not implemented yet: event-ID deduplication, alerts, MCP, or a web UI. The
current Cora Core still loads an embedded static Pack at process start; an
iterative candidate/evaluation/promotion loop is not implemented yet.

## Run locally

Start the Server:

```sh
go run ./cmd/cora-server -allow-unauthenticated -db ./cora.db
```

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
- `GET /healthz`

## Validate

```sh
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Run the reproducible Cora Pack shadow evaluation:

```sh
go run ./cmd/cora-eval \
  -input /path/to/cora-evaluation.csv \
  -product-line gbjk-zhifu \
  -json reports/cora-shadow-eval/cora-gbjk-v0-baseline.json \
  -markdown reports/cora-shadow-eval/cora-gbjk-v0-baseline.md
```

## Documentation

- [`docs/HANDOFF.md`](docs/HANDOFF.md): current truth and next development loop.
- [`docs/CORA_AGENT_V0.md`](docs/CORA_AGENT_V0.md): Agent configuration,
  delivery semantics, and Supervisor deployment.
- [`docs/CORA_V0.md`](docs/CORA_V0.md): Core contract, Cora Pack, and evaluation
  limits.
- [`docs/VISION_ALIGNMENT.md`](docs/VISION_ALIGNMENT.md): original vision versus
  current truth, including the required MCP and case loop.
- [`deploy/README.md`](deploy/README.md): Linux build, Supervisor canary,
  backup, and rollback.
- [`docs/PERFORMANCE_BASELINE.md`](docs/PERFORMANCE_BASELINE.md): reproducible
  aggregation benchmark.
