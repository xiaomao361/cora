# Deployment

This directory provides a generic Supervisor layout. Site-specific hosts, users,
paths, tokens, Packs, and topology belong in private deployment configuration.

## Build

Build only from a clean commit:

```sh
deploy/scripts/build-release.sh v0.1.0-rc1
```

The output contains statically linked Linux amd64 `cora-server`, `cora-agent`,
and `cora-canary` binaries plus `SHA256SUMS`.

## Suggested layout

```text
/opt/cora/bin/                 binaries
/etc/cora/server.yml           Server configuration
/etc/cora/auth.token           Server bearer token, mode 0600
/etc/cora/experience-packs/    private reviewed Packs
/var/lib/cora/cora.db          SQLite data
/etc/cora-agent/agent.yml      Agent configuration
/etc/cora-agent/auth.token     Agent token, mode 0600
/var/lib/cora-agent/           positions
```

Adapt the Supervisor examples under `deploy/supervisor/` to these paths and a
dedicated unprivileged user.

## Preflight

1. Copy and edit the Server and Agent examples under `config/`.
2. Create token files and persistent directories with least privilege.
3. Put private Packs outside the repository, or omit the Pack directory for
   observe-only operation.
4. Run both binaries with `-check-config`.
5. Back up the existing database and positions before replacement.
6. Verify binary `-version` output and checksums.

## Acceptance

After restart, verify `/healthz`, `/readyz`, one authenticated event batch, and
MCP initialization/tool listing. Run `cora-canary` with an explicit fictional or
private product line. Keep the previous binary, config, database backup, and
positions backup until the observation window closes.

Do not publish a deployment bundle that contains private Packs or production
configuration as a GitHub release asset.
