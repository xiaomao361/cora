# Clarion

Clarion is an agent-first error attention framework. It receives backend error
events, deterministically groups them into candidate problems, and keeps only
counts plus representative samples so downstream agents reason about problems
rather than raw error floods.

This repository is the first executable slice of the design in
`ClaraCore/seeds/lightweight-error-attention-platform.md`.

## Current vertical slice

- `POST /v1/events:batch` receives error events.
- Java-style exceptions are fingerprinted from exception type, application
  stack frames, and logger name.
- A bounded in-memory window collapses repeated events before SQLite writes.
- Each flush stores one problem update and one trend point per active
  product-line/fingerprint, plus first and latest representative samples.
- `GET /v1/problems?product_line=...` exposes candidate problems.
- `GET /v1/trends?product_line=...&fingerprint=...` exposes window counts.
- `GET /healthz` reports process health plus pending, dropped, flush-count,
  failure, event-count, and latest flush-duration metrics.
- `integrations/logback` provides the first Java production-shaped input: a
  bounded, asynchronous Logback ERROR appender with batching and hard timeouts.

Not implemented yet: Cora judgment, MCP, feedback cases, webhooks, and the debug
UI.

## Run

```sh
go run ./cmd/clarion -addr :8080 -db ./clarion.db
```

The default aggregation window is 10 seconds with at most 10,000 active
fingerprints. Configure these with `-flush-interval` and `-max-active`. When the
window is full, Clarion continues counting fingerprints already present but
drops newly seen fingerprints, increments `dropped_events`, and never blocks an
ingesting client on SQLite. A process crash can lose the current unflushed
window; SIGINT and SIGTERM trigger a final flush with a five-second timeout.

SQLite schema changes are applied at startup using `PRAGMA user_version`.
Existing unversioned Clarion databases are upgraded in place, migrations run in
transactions, and Clarion refuses to open a database created by a newer schema
version.

```sh
curl -X POST http://localhost:8080/v1/events:batch \
  -H 'content-type: application/json' \
  -d '{"events":[{"product_line":"demo","service":"orders","environment":"prod","logger":"com.example.Order","exception_type":"java.lang.OutOfMemoryError","message":"Java heap space","stacktrace":"at com.example.Order.run(Order.java:42)"}]}'
```

## Validate

```sh
go test ./...
go test -race ./...
go vet ./...
```

Reproducible aggregation benchmarks and the current Apple M4 baseline are in
[`docs/PERFORMANCE_BASELINE.md`](docs/PERFORMANCE_BASELINE.md).

The Logback appender build, configuration, failure semantics, counters, and
end-to-end example are documented in
[`integrations/logback/README.md`](integrations/logback/README.md).
