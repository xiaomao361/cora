# Cora overview

Cora is a small, agent-oriented error intelligence service. It sits between
application logs and the person or Agent responsible for deciding what an error
means.

## Data flow

```text
application logs -> Cora Agent -> Cora Server -> Problems -> MCP/HTTP review
                                                   |
                                                   +-> immutable outcome Cases
                                                   +-> offline iteration artifacts
```

The Agent parses and redacts bounded Logback events. The Server fingerprints,
aggregates, stores, and exposes them. A private product-line Pack may classify a
Problem as `attention`, `observe`, or `ignore`; the generic public Core defaults
to `observe`.

## Design boundaries

- Facts and decisions are stored separately.
- Product line is a mandatory isolation key.
- Service-scoped Problems are not merged merely because they share a trace.
- `root_cause_key` can relate manifestations without merging counts, state, or Cases.
- Trace projection is read-only evidence; it does not rewrite decisions.
- Human outcomes append Cases and may change lifecycle state, but do not
  automatically promote new production rules.
- SQLite is the v0 operational store; backups and retention changes remain explicit.

## Public Core and private Packs

The engine, protocols, schemas, and operational tools are reusable. Error rules
are not: they encode a system's class names, business semantics, and accepted
noise. Public binaries therefore contain no product-specific Pack. Deployments
load reviewed Packs from a private directory.

## Non-goals for v0

Cora is not a general log search UI, a replacement for metrics/tracing, an
automatic production-learning system, or a high-availability data platform.
Its first job is to make repeated application errors reviewable and auditable.
