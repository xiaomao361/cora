# Cora documentation

Start with these documents:

1. [`../README.md`](../README.md) for the shortest runnable introduction.
2. [`CONFIGURATION.md`](CONFIGURATION.md) before creating Server, Agent, or Pack configuration.
3. [`CORA_OVERVIEW.md`](CORA_OVERVIEW.md) for architecture and product boundaries.
4. [`deploy/README.md`](../deploy/README.md) before a production installation.
5. [`UPGRADING.md`](UPGRADING.md) before replacing an older embedded-Pack build.

Reference documents:

- [`CORA_V0.md`](CORA_V0.md): stable Core and decision contract.
- [`CORA_AGENT_V0.md`](CORA_AGENT_V0.md): log parsing and delivery behavior.
- [`RULE_ITERATION_WORKFLOW.md`](RULE_ITERATION_WORKFLOW.md): offline Pack review loop.
- [`RETENTION_AUDIT.md`](RETENTION_AUDIT.md): read-only retention evidence.
- [`PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md): release and rollout gates.
- [`ADR_001_PRODUCTION_FACT_LIFECYCLE.md`](ADR_001_PRODUCTION_FACT_LIFECYCLE.md): fact lifecycle decision.
- [`PERFORMANCE_BASELINE.md`](PERFORMANCE_BASELINE.md): reproducible local benchmark.
- [`../CHANGELOG.md`](../CHANGELOG.md): release-facing change summary.

Repository documentation describes public behavior, not a particular deployment.
Private topology, hostnames, IP addresses, business rules, evaluation data, and
historical handoffs must stay with the deployment that owns them.
