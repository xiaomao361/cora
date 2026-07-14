# Cora Supervisor deployment

This is the controlled canary path for one Cora Server and one Cora Agent.
Replace the example private IP, node identity, service, and log path before use.
Do not expose Cora Server on a public interface.

## Release layout

```text
/opt/cora/releases/<release>/cora-server
/opt/cora/releases/<release>/cora-agent
/opt/cora/releases/<release>/cora-canary
/opt/cora/current -> /opt/cora/releases/<release>
/etc/cora/auth.token
/etc/cora/agent.yml
/var/lib/cora/cora.db
/var/lib/cora-agent/positions.yml
/var/log/cora/
```

Run Server and Agent as a dedicated `cora` user. The Agent also needs read
permission for each configured application log. Keep `/etc/cora/auth.token`
owned by that user with mode `0600`; distribute the same file to the Server and
authorized Agent hosts over the existing secure administration channel.

Generate a token without printing it:

```sh
umask 077
openssl rand -hex 32 > /etc/cora/auth.token
```

## Build identified Linux amd64 binaries

Build only from a clean, identified release commit. The release script injects
version, commit, and UTC build time into all three binaries and writes checksums:

```sh
go test ./...
go test -race ./...
go vet ./...
deploy/scripts/build-release.sh v0.1.0
cat dist/v0.1.0/SHA256SUMS
```

Copy the binaries into a new immutable release directory, then update the
`/opt/cora/current` symlink. Do not overwrite the previous release.

## Server boundary

Edit `deploy/supervisor/cora-server.conf` and replace `10.0.0.10` with the
Server's private address. The process refuses to start without
`-auth-token-file` unless `-allow-unauthenticated` is explicitly used for local
development. Restrict the Server security group/firewall to only the selected
Agent private addresses. `/healthz` is intentionally unauthenticated; every
other current or future endpoint, including `/v1/*` and `/mcp`, requires the token.

The Agent-facing MCP endpoint is `http://<private-server-address>:8080/mcp` and
uses Streamable HTTP. Configure the consuming Agent with that URL and the same
`Authorization: Bearer <token>` header; keep the token in the Agent's secret or
credential facility rather than checking it into an MCP configuration file.

## Agent boundary

Copy `config/cora-agent-canary.example.yml` to `/etc/cora/agent.yml` and replace:

- Server private address;
- `product_line`, `app`, `node`, and `deployment_group`;
- the explicit active Logback file path.

Keep `agent.start_at: end` for the first canary so deployment does not replay
historical logs. Validate before Supervisor starts it:

```sh
/opt/cora/current/cora-agent -config.file=/etc/cora/agent.yml -check-config
```

## Install and operate with Supervisor

Copy only the relevant program file to the host's Supervisor include directory,
then run:

```sh
supervisorctl reread
supervisorctl update
supervisorctl status cora-server
supervisorctl status cora-agent
```

Server and Agent handle `SIGTERM`. Agent delivery failures exhaust bounded
retries and exit non-zero, so `autorestart=unexpected` restarts it from the last
acknowledged position.

Before starting a new Server binary against an existing database, create a
verified consistent backup with the currently deployed binary:

```sh
deploy/scripts/backup-server.sh \
  /opt/cora/current/cora-server \
  /var/lib/cora/cora.db \
  /var/backups/cora/server
```

Before replacing an Agent, stop it briefly and back up its acknowledged offsets:

```sh
supervisorctl stop cora-agent
deploy/scripts/backup-positions.sh \
  /var/lib/cora-agent/positions.yml \
  /var/backups/cora/agent
supervisorctl start cora-agent
```

Canary liveness and readiness:

```sh
curl --fail http://127.0.0.1:9088/readyz
curl --fail http://10.0.0.10:8080/healthz

/opt/cora/current/cora-canary \
  -server-url=http://10.0.0.10:8080 \
  -auth-token-file=/etc/cora/auth.token \
  -product-line=gbjk-zhifu
```

Also verify that an unauthenticated `/v1/problems` request returns `401`, the
Agent advances its positions file, Cora creates the expected service Problem,
and Server health reports zero dropped events, no unrecovered SQLite write
failure, the expected schema/build identity, and current write timestamps. Before
expanding the canary, use a real MCP client to call `cora_list_attention`,
`cora_get_problem`, and `cora_record_outcome`, then confirm the resolved Problem
disappears from the current list and a later matching error returns as recurring.

## Restore drill and rollback

The backup command uses SQLite `VACUUM INTO` and immediately runs
`PRAGMA quick_check` against the result. Exercise a restore before the canary:

```sh
supervisorctl stop cora-server
mv /var/lib/cora/cora.db /var/lib/cora/cora.db.pre-restore
cp /var/backups/cora/server/<timestamp>/cora.db /var/lib/cora/cora.db
chown cora:cora /var/lib/cora/cora.db
chmod 0600 /var/lib/cora/cora.db
/opt/cora/current/cora-server -db=/var/lib/cora/cora.db -check-db
supervisorctl start cora-server
/opt/cora/current/cora-canary \
  -server-url=http://10.0.0.10:8080 \
  -auth-token-file=/etc/cora/auth.token \
  -product-line=gbjk-zhifu
```

Rollback by stopping the program, pointing `/opt/cora/current` at the previous
immutable release, restoring the matching database backup if the schema changed,
and starting the program again. Preserve the failed release, logs, database,
and positions until the incident is understood.

## 72-hour production test gate

Start with one Agent and one or two explicit files. Keep `start_at: end`. During
the 72-hour canary, check at least daily:

- Agent readiness is 200; all workers are running/readable and delivery is not failing.
- Per-target lag returns toward zero; retry, parse, truncation, and drop counters are reviewed.
- Server readiness is 200; the latest SQLite write is successful and schema/build identity is expected.
- Supervisor has no restart loop and disk usage for logs, SQLite, WAL, and positions remains bounded.
- `cora-canary` passes and an actual Agent completes one list/get/record outcome loop.
- At least one worthwhile problem is either fixed or deliberately classified; complete coverage is not required.

Stop expansion if delivery remains failing, lag grows continuously, SQLite's
latest write is failed, sensitive data appears in a sample, product-line facts
mix, or backup restore cannot be reproduced.
