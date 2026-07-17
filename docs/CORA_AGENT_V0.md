# Cora Agent v0

Cora Agent tails one or more local Logback files and sends normalized Events to
`POST /v1/events:batch`.

It supports multiline Java stack traces, per-target labels, persistent positions,
bounded breadcrumbs, batching, retries, log rotation, readiness, and redaction.
An offset is committed only after the Server accepts the batch.

Use the checked-in examples:

- `config/cora-agent.example.yml`: one fictional service.
- `config/cora-agent-multi.example.yml`: two fictional services.

Validate configuration with:

```sh
cora-agent -config.file=/etc/cora-agent/agent.yml -check-config
```

The Agent requires each target to define a product line, service identity through
the `app` label, and a log path. `node` and `deployment_group` are recommended for
multi-node analysis. Tokens and real paths must stay outside Git.

Readiness becomes degraded when a target is unreadable, a worker stops, delivery
retries are exhausted, or acknowledged positions lag behind the file. See
`docs/CONFIGURATION.md` for field guidance.
