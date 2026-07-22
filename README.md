# Cora

Cora turns application error logs into durable, reviewable Problems. It keeps raw
occurrence facts separate from decisions, exposes the result through HTTP and MCP,
and records human outcomes as immutable Cases.

The public repository contains only the generic engine. Product-specific error
rules are private deployment data and are loaded from an explicit external Pack
directory. Without a Pack, Cora safely keeps every product line in `observe`.

## Components

- `cora-server`: ingestion, SQLite storage, HTTP query APIs, and MCP.
- `cora-agent`: tails one or more Logback files and sends bounded event batches.
- `cora-canary`: read-only health and MCP acceptance probe.
- `cora-iterate`: exports a stable case snapshot and produces review artifacts.
- `cora-retention-audit`: audits a consistent backup without mutating it.
- `cora-eval`: evaluates a private Pack against a labeled CSV dataset.

## Quick start

Run an observe-only local Server:

```sh
go run ./cmd/cora-server \
  -addr 127.0.0.1:8080 \
  -db /tmp/cora.db \
  -allow-unauthenticated
```

Run it with the fictional example Pack:

```sh
go run ./cmd/cora-server \
  -addr 127.0.0.1:8080 \
  -db /tmp/cora.db \
  -allow-unauthenticated \
  -experience-pack-dir config/experience-packs
```

For a real deployment, copy the examples under `config/`, replace every
`example.com` path and identity, keep the bearer token outside Git, and keep real
experience Packs in a private directory.

## Interfaces

- `POST /v1/events:batch`
- `GET /v1/problems?product_line=<line>`
- `GET /v1/attention?product_line=<line>`
- `GET /v1/trends?product_line=<line>&service=<service>&fingerprint=<fingerprint>`
- `GET /v1/node-occurrences?product_line=<line>&service=<service>&fingerprint=<fingerprint>`
- `GET /v1/node-trends?product_line=<line>&service=<service>&fingerprint=<fingerprint>`
- `GET /healthz` and `GET /readyz`
- `POST /mcp` using Streamable HTTP

All fact and MCP queries require an explicit product line. Problem detail and
outcome writes also require service and fingerprint; `root_cause_key` can select
one cause when a fingerprint has split into multiple Problems.

The MCP surface also exposes `cora_retrieve_cases` for deterministic, read-only
retrieval of similar handled Cases when the selected Problem is currently
`cora.default.unmatched`. The retrieved Case outcomes are evidence only and do
not replace the stored Core decision.

## Validation

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
git diff --check
```

After committing a clean release boundary:

```sh
deploy/scripts/build-release.sh v0.1.0-rc1
```

The build script produces identified Linux amd64 binaries and SHA-256 checksums.

## Documentation

- [Documentation map](docs/README.md)
- [Configuration guide](docs/CONFIGURATION.md)
- [Architecture and product boundary](docs/CORA_OVERVIEW.md)
- [Core v0 contract](docs/CORA_V0.md)
- [Agent guide](docs/CORA_AGENT_V0.md)
- [Deployment guide](deploy/README.md)
- [Upgrade guide](docs/UPGRADING.md)
- [Rule iteration workflow](docs/RULE_ITERATION_WORKFLOW.md)
- [Retention audit](docs/RETENTION_AUDIT.md)

Real production topology, product-line Packs, evaluation datasets, outcomes, and
operational handoffs do not belong in this public repository.

## License

Cora is available under the [MIT License](LICENSE).
