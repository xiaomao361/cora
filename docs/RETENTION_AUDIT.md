# Retention audit

Retention starts with read-only evidence. Run `cora-retention-audit` against a
consistent SQLite backup, never the live writable database:

```sh
cora-retention-audit \
  -db /secure/backups/cora.db \
  -product-line payments \
  -cora-build-version v0.1.0-rc1 \
  -cora-source-digest <sha256> \
  -iteration-root /private/cora/iterations \
  -closure-root /private/cora/closure-receipts \
  -run-id payments-20260717T000000Z
```

The command opens SQLite read-only, runs `PRAGMA quick_check`, records database
identity, verifies referenced artifacts and closure receipts, and explains why
each Problem is or is not eligible. It estimates logical release only; it does
not delete rows, checkpoint WAL, vacuum, or authorize cleanup.

The MCP `cora_retention_audit` tool is a live preflight with a narrower evidence
boundary. A consistent-backup audit remains required before mutation.
