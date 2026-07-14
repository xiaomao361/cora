# Cora vision alignment

This document compares the current repository with the original direction in
`ClaraCore/seeds/lightweight-error-attention-platform.md`. The later Cora naming
decision remains authoritative: Cora is the product, Cora Server contains the
stable framework, and Cora Core is the independently evolving judgment layer.

## Current alignment

| Original direction | Current truth | Assessment |
|---|---|---|
| Collapse an error flood before judgment | bounded fingerprint window, count/trend upserts, representative samples | aligned |
| Low-resource, self-hosted, no MQ/Redis | static Go binaries plus SQLite WAL | aligned |
| Product-line-isolated experience | explicit `product_line` and isolated Pack selection | aligned |
| Preserve context without building APM | stacktrace, bounded breadcrumbs, trace/thread fallback, redaction | aligned |
| Framework should split rather than over-merge | Problem identity includes product line, service, and fingerprint | aligned |
| Existing logs are the practical first input | one Agent per host tails explicit Logback files | intentional adaptation from the earlier Appender assumption |
| MCP is the Agent First primary interface | only HTTP ingest/query APIs exist | critical missing surface |
| Agent handling results become structured cases | feedback schemas exist, but no persistence or write path | critical missing loop |
| Core iterates through rules, LLM gray-zone judgment, and case retrieval | only an embedded static rule Pack runs today | critical missing Core stages |
| Trigger Core on meaningful state changes | Core runs at aggregate flush; no EWMA burst, impact-expansion event, or Problem state machine | incomplete |
| Webhook later, Web UI only for debugging | neither exists | correctly deferred |

The current system is therefore a credible ingestion and deterministic-fact
foundation, but not yet the complete Agent First product. A technical canary can
validate collection and storage. A product canary requires the MCP feedback loop.

## Next product slice: MCP plus cases

Cora Server should host Streamable HTTP MCP in the same process; this does not
justify another service. The existing bearer token and private listener should
protect both HTTP and MCP surfaces.

The first MCP slice should stay small:

1. `cora_list_attention`: list current attention/observe Problems for one
   explicit product line, with decision reason and freshness.
2. `cora_get_problem`: return representative samples, trends, node distribution,
   release/environment context, and prior cases for one service-scoped Problem.
3. `cora_record_outcome`: write the original four-field result -- real problem,
   handled, one-line root cause, and one-line action -- against the Problem and
   acting Agent.

The write must create an immutable, product-line-scoped case snapshot. Query and
write belong to the same MCP server so an Agent can pull, investigate, and close
the loop without switching products.

## Core iteration without drift

The original iteration mechanism is more specific than generic online learning:

```text
rules catch stable fast paths
-> LLM judges the remaining gray zone with current facts and retrieved cases
-> Agent writes the real outcome
-> case retrieval improves the next judgment immediately
-> repeated consistent cases can be proposed as a human-reviewed rule
-> a small learned filter is considered only after the per-line case gate
```

Rules are therefore one layer and one form of crystallized experience, not the
whole Core. Initial iteration means accumulating cases, improving retrieval and
prompts, and promoting reviewed hard rules. It does not mean allowing production
decisions to retrain and activate themselves without evaluation or rollback.

## Guardrails

- Do not put raw error floods in MCP or send them directly to an LLM.
- Do not mix cases or rules across product lines by default.
- Do not build a dashboard before the Agent workflow is complete.
- Do not split MCP, storage, and ingest into separate services.
- Do not treat static Pack hot reload alone as the Core learning loop.
- Do not let an `ignore` decision erase facts or hide frequency bursts.
- Do not expand into APM, full tracing, or notification-channel integrations.
