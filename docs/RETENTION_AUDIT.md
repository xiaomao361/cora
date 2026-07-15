# Cora B0 retention audit

`cora-retention-audit` explains which production facts are retention-eligible and why. It is a
read-only B0 tool: it cannot migrate, compact, delete, checkpoint, purge, or vacuum SQLite.

## MCP live preflight

The Server also exposes `cora_retention_audit` for routine agent use. It opens one read-only
transaction against the live Store and returns:

- full-product-line counts for resolved Problems, Problems with handled Cases, and Problems ready
  to enter the forensic audit;
- stable, ID-paged Problem facts and blocking reason codes;
- an explicit `forensic_audit_required=true` boundary.

This online tool intentionally cannot see local iteration artifacts or closure receipts. Every
Problem therefore retains the blocker
`closure_receipt_verification_requires_offline_audit`, and the online result never sets
`retention_eligible=true`. It is a convenient lifecycle preflight, not a cleanup receipt. Before
compact or purge, run the standalone command below against a consistent backup and preserve its
hashed outputs.

## Input boundary

Use a SQLite file created by Cora's consistent backup command and already handled as sensitive
production data. Do not copy a live `cora.db` file without its WAL. The audit opens the supplied
file through SQLite `mode=ro&immutable=1`, runs `PRAGMA quick_check`, and never calls
`cora.OpenStore`, whose normal Server path configures WAL and runs migrations. `immutable=1` is
safe only because this command requires a completed consistent backup; it prevents SQLite from
creating or touching WAL/SHM sidecars during audit.

The product line is mandatory. Every table query is filtered by that line; receipts belonging to
other product lines are ignored. The iteration and closure roots are local immutable evidence
roots. A closure receipt is not trusted merely because its JSON says `retention_eligible=true`.

```sh
go run ./cmd/cora-retention-audit \
  -db /secure/backups/cora-20260715T040000Z.db \
  -product-line gbjk-zhifu \
  -cora-build-version v0.1.0-rc6-dirty \
  -cora-source-digest 7d69633fd551a0ee6bdecbd167f9fe35412e1627a8729ef02ee5a2b376ba278e \
  -iteration-root out/iterations \
  -closure-root out/closure-receipts \
  -output-root out/retention-audits \
  -run-id gbjk-zhifu-20260715T040000Z
```

The command refuses to overwrite an existing audit directory. Its capture identity is the backup
file's modification time, so the same unchanged database and evidence roots produce byte-stable
artifacts for the same run ID.

## Eligibility gates

A Problem is eligible only when all of these can be verified:

1. The Problem is `resolved` and has handled Case evidence.
2. A strict `cora.closure-receipt.v1` matches its product line, service, and fingerprint.
3. The receipt's case IDs exist on that Problem and are handled.
4. The case manifest SHA-256 resolves to a real file, its identity matches the receipt, and its
   referenced case snapshot SHA-256 also resolves.
5. Reviewed Pack, passed evaluation, deployed build, and passed observation evidence hashes all
   resolve to real files.
6. The observation window ended before the backup capture and no Problem occurrence is newer than
   that window.

Missing, malformed, mismatched, or unverifiable evidence is conservative: it adds stable blocking
reason codes and leaves the Problem ineligible. If several receipts exist, the audit chooses the
one with the fewest blockers, then the lexically smallest receipt ID for deterministic output.

## Output

```text
out/retention-audits/<product_line>/<audit_run_id>/
  audit.json
  audit.md
  problem-decisions.jsonl
  run.json
```

`audit.json` follows `schemas/cora-retention-audit.v1.schema.json`. JSON, Markdown, and JSONL are
rendered from the same in-memory result. `run.json` records the database and the receipt/artifact
hashes actually used, plus hashes for every output artifact.

The report includes database SHA-256, size, schema version, backup capture identity, quick-check,
page/freelist/WAL facts, product-line table counts and time ranges, state/decision/handled/Case
breakdowns, and every Problem's eligibility reasons.

The logical release estimate counts UTF-8 payload bytes in eligible first/latest samples plus text
fields and 8 bytes per integer in eligible trend rows. It deliberately excludes SQLite record
headers, indexes, B-tree overhead, and retained Problem identity rows. It is not an estimate of
immediate file shrink: deletion creates reusable pages, while physical shrink belongs to a later,
separately controlled checkpoint/VACUUM phase.

## Production acceptance

Before any B1/B2 work, run B0 against an authorized consistent production backup and preserve:

- the backup's SHA-256, size, mtime, and `quick_check=ok` before/after;
- the complete audit directory and recomputed output hashes;
- the deployed Cora build version and full source digest associated with the backup time;
- confirmation that the live primary database was not used as the audit target.

Fixture validation alone proves the implementation boundary, not production acceptance.
