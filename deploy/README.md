# Cora Supervisor deployment

This is the controlled canary path for one Cora Server and one Cora Agent.
Replace the example private IP, node identity, service, and log path before use.
Do not expose Cora Server on a public interface.

## Flat runtime layout

```text
/home/cora/cora-server
/home/cora/cora-agent
/home/cora/auth.token
/home/cora/cora.yml
/home/cora/agent.yml
/home/cora/cora.db
/home/cora/positions.json
/home/cora/cora-server.log
/home/cora/cora-agent.log
```

Run Server and Agent as a dedicated `cora` user. The Agent also needs read
permission for each configured application log. Keep `/home/cora/auth.token`
owned by that user with mode `0600`; distribute the same file to the Server and
authorized Agent hosts over the existing secure administration channel.

Generate a token without printing it:

```sh
umask 077
install -d -o cora -g cora -m 0750 /home/cora
openssl rand -hex 32 > /home/cora/auth.token
chown cora:cora /home/cora/auth.token
chmod 0600 /home/cora/auth.token
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

Copy only the binary needed by each host directly into `/home/cora`: Server
hosts need `cora-server`; application hosts need `cora-agent`. `cora-canary` is
an optional acceptance tool, not a runtime dependency. Before replacement, keep
a `.previous` copy of the currently running binary for simple rollback.

## Server boundary

Copy `config/cora-server.example.yml` to `/home/cora/cora.yml`, then replace
`10.0.0.10` with the Server's private address. The production command has one
configuration argument:

```sh
cd /home/cora
./cora-server -config.file=./cora.yml -check-config
./cora-server -config.file=./cora.yml
```

Relative paths in `cora.yml` resolve from the process working directory. The
Supervisor program therefore fixes `directory=/home/cora`, so `./cora.db` and
`./auth.token` remain beside the binary and configuration. The process refuses
to start without `auth.bearer_token_file` unless
`auth.allow_unauthenticated: true` is explicitly used for local development.

Restrict the Server security group/firewall to only the selected Agent private
addresses. `/healthz` is intentionally unauthenticated; every other current or
future endpoint, including `/v1/*` and `/mcp`, requires the token.

The Agent-facing MCP endpoint is `http://<private-server-address>:8080/mcp` and
uses Streamable HTTP. Configure the consuming Agent with that URL and the same
`Authorization: Bearer <token>` header; keep the token in the Agent's secret or
credential facility rather than checking it into an MCP configuration file.

## Agent boundary

Copy `config/cora-agent-canary.example.yml` to `/home/cora/agent.yml` and replace:

- Server private address;
- `product_line`, `app`, `node`, and `deployment_group`;
- the explicit active Logback file path.

Keep `agent.start_at: end` for the first canary so deployment does not replay
historical logs. Validate before Supervisor starts it:

```sh
cd /home/cora
./cora-agent -config.file=./agent.yml -check-config
```

## Install and operate with Supervisor

Keep `cora-server.conf` or `cora-agent.conf` directly in `/home/cora`, and link
the relevant file into the host's Supervisor include directory. Then run:

```sh
ln -s /home/cora/cora-server.conf /etc/supervisor/conf.d/cora-server.conf
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
  /home/cora/cora-server \
  /home/cora/cora.db \
  /home/cora
```

Before replacing an Agent, stop it briefly and back up its acknowledged offsets:

```sh
supervisorctl stop cora-agent
deploy/scripts/backup-positions.sh \
  /home/cora/positions.json \
  /home/cora
supervisorctl start cora-agent
```

Canary liveness and readiness:

```sh
curl --fail http://127.0.0.1:9088/readyz
curl --fail http://10.0.0.10:8080/healthz

/home/cora/cora-canary \
  -server-url=http://10.0.0.10:8080 \
  -auth-token-file=/home/cora/auth.token \
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
mv /home/cora/cora.db /home/cora/cora.db.pre-restore
cp /home/cora/cora-<timestamp>.db /home/cora/cora.db
chown cora:cora /home/cora/cora.db
chmod 0600 /home/cora/cora.db
cd /home/cora
./cora-server -config.file=./cora.yml -check-db
supervisorctl start cora-server
/home/cora/cora-canary \
  -server-url=http://10.0.0.10:8080 \
  -auth-token-file=/home/cora/auth.token \
  -product-line=gbjk-zhifu
```

Rollback by stopping the program, restoring its `.previous` binary and the
matching flat database backup if the schema changed, then starting it again.
Preserve the failed binary, logs, database, and positions until the incident is
understood.

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
