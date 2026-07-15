# Cora Agent v0

Cora Agent is a small, standalone Go process for teams that cannot add a
logging SDK or change an application release. One process can follow many
active Logback files, reconstruct multiline ERROR events, and send Cora's
existing batch ingest contract. It does not require Loki.

## Scope

Agent v0 deliberately implements only the operational core needed by
`gbjk-order`:

- many explicit active-file targets per process;
- the production Logback text pattern documented below;
- multiline Java stacktraces;
- bounded pre-error breadcrumbs using trace ID or thread correlation;
- upload-time redaction of credential keys and common sensitive number shapes;
- durable byte positions with atomic `0600` writes;
- rename rotation and copy-truncate detection;
- count- and byte-bounded batches;
- retry for connection failures, HTTP 429, and HTTP 5xx;
- position commit only after a successful 2xx response.
- local `/healthz` liveness and `/readyz` file-readiness endpoints.

It does not implement globs, service discovery, Kubernetes metadata, arbitrary
pipelines, compressed-history replay, a query API, or Loki's wire protocol.

## Promtail-style configuration

The recommended mode is one YAML file for all services on a host:

```yaml
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 9088
  grpc_listen_port: 0

positions:
  filename: /home/cora/positions.json

clients:
  - url: http://cora.internal:8080/v1/events:batch
    bearer_token_file: /home/cora/auth.token

defaults:
  product_line: qikang-zhifu
  environment: prod
  timezone: Asia/Shanghai

scrape_configs:
  - job_name: gb-order_log_push
    static_configs:
      - labels:
          app: gb-order
          server: backup
          ip: 172.16.0.229
          env: prod
          group: platform
          __path__: /home/qikang/data/gb-order/supervisor.log
```

`app` becomes Cora `service`, `env` becomes `environment`, and `__path__`
selects the file. `product_line` may be set globally in `defaults` or overridden
per target. All other labels, plus `job`, travel with the representative event
but do not affect its fingerprint.

The checked-in Qikang example converts all 18 targets from the provided
Promtail configuration:

```sh
go run ./cmd/cora-agent \
  -config.file config/cora-agent-qikang.example.yml \
  -check-config

# configuration valid: 18 targets
```

The supported YAML subset is intentionally strict. A Loki push URL is rejected;
`clients[0].url` must target Cora `/v1/events:batch`. Duplicate paths and
path globs are also rejected so one file cannot be silently double-counted.
`${ENV_VAR}` placeholders are expanded before YAML parsing, so endpoint,
positions, file paths, and ports can be supplied by Supervisor or a container
runtime without duplicating configuration files.

## Supported Logback pattern

```text
%d{yyyy-MM-dd HH:mm:ss.SSS} trace_id: %X{__trace_id} [%thread] %-5level %logger{20} - [%method,%line] - %msg%n%ex
```

The parser uploads only ERROR records, while retaining a bounded ring of prior
records for context. It extracts timestamp, trace ID, thread, logger, method,
line, message, exception type, and stacktrace. When an ERROR has no Throwable,
the logger and method become a synthetic top frame so method-based Cora rules
remain usable. Trace ID and source filename are correlation metadata and never
participate in Cora fingerprinting.

For an ERROR with a trace ID, the Agent attaches at most 20 preceding records
from the same trace in the prior 30 seconds. Without a trace ID, it falls back
to at most five records from the same thread in the prior five seconds. Each
target owns an independent 16 KiB ring that survives active-file reopen. The
Agent does not wait for post-error logs, and breadcrumbs never participate in
fingerprinting.

Immediately before JSON encoding, the Agent redacts `Authorization`, token,
password, and `cardNo`-style key/value fields, signed OSS/S3 URL query
credentials, mainland mobile-number shapes, and 18-character identity-number
shapes. Redaction covers the ERROR message, stacktrace, breadcrumbs, and labels;
raw values are not sent to Cora Server.

Operational stdout/stderr logs record process and target lifecycle, file
open/reopen, batch counts and byte sizes, delivery status, retry backoff, and a
final per-target counter summary. They deliberately omit event messages,
stacktraces, breadcrumbs, labels, bearer tokens, and request bodies.

## Run locally

Start Cora, then the agent using YAML:

```sh
go run ./cmd/cora-server -allow-unauthenticated -db /tmp/cora.db -flush-interval 1s

go run ./cmd/cora-agent -config.file /home/cora/agent.yml
```

The original single-file flags remain available for local smoke tests.

YAML mode reports one `target_statuses[]` entry per configured file. It includes
`running`, `readable`, file size, acknowledged offset, byte lag, parsed/error/
truncated counts, sent events, retries, final delivery failures, and the latest
read/delivery/failure times. `/healthz` remains liveness. `/readyz` returns 503
when a worker is not running, a file is unreadable, or delivery is currently
failing. Lag and parse/truncation remain visible without imposing an arbitrary
production threshold before the canary has real baseline data.

For an unseen file, the default is to start at its current end so installing
the agent cannot unexpectedly replay months of logs. Use `-from-start` only for
a controlled test or historical import.

Historical events may arrive out of order, including newest-first Loki exports.
Cora compares event timestamps when aggregating, so `first_seen`, `last_seen`,
`first_sample`, and `latest_sample` remain chronological rather than reflecting
arrival order.

Defaults retain at most 256 KiB for the ERROR record plus a separately bounded
16 KiB breadcrumb context, and send JSON requests no larger than 1.5 MiB, below
Cora's 2 MiB ingest limit. A batch contains at most 100 events.

## Supervisor deployment

Production uses the checked-in `deploy/supervisor/cora-agent.conf`. The Agent
configuration reads the shared bearer secret from `clients[].bearer_token_file`;
the token does not appear in the Supervisor command line or YAML. Full Linux
build, install, canary, backup, and rollback instructions are in
`deploy/README.md`.

The service account needs read access to the active log and write access only
to its positions directory.

## Delivery semantics

Positions represent server-acknowledged progress, not merely bytes read. A
failed batch does not advance the position; after bounded retries the process
exits so Supervisor can restart it from the last acknowledged offset. This avoids
silent loss but is intentionally at-least-once: if Cora accepts a request
and the response is lost, the restarted agent can send that batch again.

All targets in one process share a concurrency-safe positions file. Only one
agent process may own that file. Deleting it intentionally
resets history; with the default start mode, the agent then resumes at the active
file's current end.

## Verified multi-target result

A real-process smoke used one YAML file with `gb-auth` and `gb-order` targets,
two production-format files, one shared positions file, and a local Cora:

- Agent health: `status=ok`, `targets=2`, with two target status records.
- Agent readiness: `status=ready`, `readable_targets=2`, with delivery healthy
  and acknowledged lag returning to zero.
- Cora Problems: two, services `gb-auth` and `gb-order`.
- Position entries: two independent acknowledged offsets.
- Cora: both `observe / cora.default.untrained-product-line`, because the
  Qikang product line correctly cannot inherit the Guanbai experience pack.

## Promtail reference boundary

The design was checked against Promtail's official positions, file tailer, and
batch client implementations. Cora reuses the proven concepts—not their
code or full product surface—and tightens the checkpoint rule around successful
delivery. Promtail itself is now feature-complete in Grafana's stack, which is
another reason not to build a general replacement here.
