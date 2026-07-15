# ADR 001: Production fact lifecycle and closure receipts

- Status: Accepted for the read-only iteration and retention-audit phases
- Date: 2026-07-15
- Scope: Cora production SQLite, local iteration artifacts, and future retention work

## Context

Cora intentionally collapses repeated ERROR events into service-scoped Problems. The
production SQLite database therefore holds a hot operational working set, rather than a
complete log archive. It also contains growing trend data, representative samples, and
immutable case snapshots. Keeping all of those forever makes the production database an
accidental history warehouse; deleting them without durable evidence would make rule
decisions and recurrence impossible to audit.

Retention depends on a stronger boundary than `Problem.state = resolved`. A fact is only
eligible after its case export is complete and verified, a reviewed rule has passed a frozen
evaluation, the rule has been deployed, and the observation window has passed. Those facts
must be joined by a machine-verifiable closure receipt.

`problem_cases.problem_id` currently has a foreign key to `problems.id`. Hard-deleting a
Problem would either violate that relationship or require an irreversible migration before
the export and restore path has been exercised.

## Decision

### 1. Split hot operational facts from durable iteration facts

Production SQLite is the source of truth for active Problem state, current counters,
representative samples, recent trends, and the Server's current decisions.

The local immutable artifact directory under `out/iterations/<product_line>/...` is the
long-term source of truth for frozen case exports, triage, rule candidates, shadow
evaluations, and closure receipts. Every artifact is content-addressed with SHA-256 and is
scoped to one explicit `product_line`. `out/` remains ignored by Git; reviewed Pack changes,
schemas, concise reports, and documentation remain Git-owned.

The first implementation does not copy raw log lines, bearer tokens, signed URL secrets, or
production SQLite files into iteration artifacts.

### 2. Use three versioned protocol records

- `cora.iteration-run.v1` freezes run identity, product line, business window, Cora build,
  Pack identity, case-export pages, and artifact hashes.
- `cora.rule-candidate.v1` records the exact Pack-compatible matcher, intended decision,
  source case IDs, evaluation evidence, and false-positive/false-negative risk.
- `cora.closure-receipt.v1` links the verified case snapshot, reviewed rule, passed
  evaluation, deployed build, and completed observation window.

`retention_eligible=true` is valid only when all four gates in the closure receipt are
complete. A rule's existence, a resolved state, or a handwritten note is insufficient.

### 3. Keep phase B0 read-only and keep provenance local in phase B1 design

The first retention command is an audit only. It may use a read-only connection or a
consistent backup, but it must not issue `DELETE`, `UPDATE`, checkpoint, or `VACUUM` against
the production database.

Before mutations are implemented, B1 will add a minimal production provenance index that
stores closure-receipt identity and digest, not the full long-term artifacts. The exact
SQLite migration is intentionally deferred until a real iteration run and retention audit
have demonstrated the required query keys.

### 4. Compact to a tombstone before considering hard deletion

B2 will preserve the `problems` row and its stable identity. Eligible resolved Problems may
lose old fine-grained trends and large representative samples, but retain at least:

- `id`, `product_line`, `service`, and `fingerprint`;
- lifetime count and first/last seen times;
- resolved/retention state and the closure-receipt digest;
- the last deployed rule and Pack identity needed for recurrence provenance.

This minimal row is the tombstone. It preserves the current foreign key and avoids making a
restore depend on a local artifact being online during ingestion. In this design,
`purged_from_production` means that bulky hot-workset payloads have been purged; it does not
mean that the identity tombstone has been removed.

Hard deletion of the tombstone is outside B2. B3 may reconsider it only after an explicit
foreign-key migration, backup/restore drill, and recurrence test prove that identity and
provenance survive.

### 5. Rehydrate the same identity on recurrence

A genuinely newer event with the same `product_line + service + fingerprint` reuses the
tombstone Problem ID, changes the Problem to `recurring`, stores fresh representative
samples and recent trends, and retains the prior closure/rule reference. Historical replay
at or before the prior handled time must not reopen it.

Recurrence immediately makes the Problem retention-ineligible. A later closure requires a
new iteration run and a new closure receipt; an old receipt remains immutable evidence of
the earlier cycle.

## Consequences

- Retention remains conservative: identity rows may be numerous, but their size is bounded
  and much smaller than samples and time-series data.
- Local artifacts must be backed up and hash-verified because they become the durable audit
  source.
- Physical file shrink is a separate maintenance operation. Logical deletion only creates
  reusable SQLite pages; checkpoint and `VACUUM` require their own capacity and downtime
  controls.
- Schema v5 is unchanged in this phase. No production mutation is authorized by this ADR.

## Acceptance checks for the next implementation slice

1. The same frozen case snapshot produces byte-stable manifests and candidate evaluation.
2. A retention audit explains every eligible and ineligible count without writing SQLite.
3. A receipt marked retention-eligible validates all four gates and artifact hashes.
4. A tombstone recurrence test preserves Problem identity and prior rule provenance.
