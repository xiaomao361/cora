# Upgrading

## Moving from an embedded-Pack build

Older development builds loaded a product-specific Pack compiled into the
binary. Public Cora releases no longer embed any production Pack.

Before replacing such a Server:

1. Export the reviewed Pack to a private directory on the target host.
2. Add `core.experience_pack_dir` to the private Server YAML.
3. Run `cora-server -config.file=... -check-config` as the runtime user.
4. Back up SQLite and retain the previous binary/config.
5. Start the new binary and confirm build identity, schema version, Pack-backed
   decisions, authenticated ingest, and MCP reads with a canary product line.

If `experience_pack_dir` is omitted, startup succeeds and every product line
uses the framework-default `observe` decision. This prevents private rules from
appearing implicitly, but it is a behavior change that must not be mistaken for
a successful Pack deployment.

## Database migration

Opening the database migrates it to the current schema. Back up the consistent
database first. Do not attempt to roll an older binary forward against a database
whose schema is newer than that binary supports; restore the paired backup when
rolling back.

## Public/private artifact split

Do not copy production Packs, model manifests, evaluation reports, topology, or
historical handoffs back into the public repository during an upgrade. Keep a
private deployment inventory that records Pack version/SHA-256 alongside the
public Cora build identity.
