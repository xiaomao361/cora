# Production readiness

A release candidate is ready for deployment only when:

- the intended code and public docs form one clean commit;
- no private Pack, topology, token, production report, or real error fixture is tracked;
- tests, race tests, vet, build, formatting, and diff checks pass;
- Linux release artifacts are built from the clean commit with identity and checksums;
- Server and Agent example configurations validate after site values are supplied;
- the database is backed up and the rollback binary/config are known;
- canary verifies health, readiness, authenticated ingest, and MCP read paths.

Deployment readiness is separate from public release readiness. A generic public
release may be valid while a private Pack or production rollout is still under
review. Conversely, a private internal binary containing a Pack must not be
published as a public release artifact.

The v0 acceptance model permits short interruptions and bounded event loss during
explicit recovery, but not silent permanent ingestion failure. Agent readiness,
Server write health, backup restore, rotation, retry, and disk-pressure behavior
must be tested in the target environment.
