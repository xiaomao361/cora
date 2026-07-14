# Cora Supervisor deployment

This is the controlled canary path for one Cora Server and one Cora Agent.
Replace the example private IP, node identity, service, and log path before use.
Do not expose Cora Server on a public interface.

## Release layout

```text
/opt/cora/releases/<release>/cora-server
/opt/cora/releases/<release>/cora-agent
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

## Build Linux amd64 binaries

Build from a clean, identified release commit:

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/cora-server-linux-amd64 ./cmd/cora-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/cora-agent-linux-amd64 ./cmd/cora-agent
sha256sum dist/cora-*-linux-amd64
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

Canary acceptance:

```sh
curl --fail http://127.0.0.1:9088/readyz
curl --fail http://10.0.0.10:8080/healthz
```

Also verify that an unauthenticated `/v1/problems` request returns `401`, the
Agent advances its positions file, Cora creates the expected service Problem,
and Server health reports zero dropped events and flush failures.

## Backup and rollback

Before replacing a Server release:

```sh
supervisorctl stop cora-server
backup_dir=/var/backups/cora/$(date +%Y%m%d-%H%M%S)
mkdir -p "$backup_dir"
cp -a /var/lib/cora/cora.db* "$backup_dir"/
supervisorctl start cora-server
```

Before replacing an Agent release, stop it and copy its positions file. Never
delete positions as part of rollback.

Rollback by stopping the program, pointing `/opt/cora/current` at the previous
immutable release, restoring the matching database backup if the schema changed,
and starting the program again. Preserve the failed release, logs, database,
and positions until the incident is understood.
