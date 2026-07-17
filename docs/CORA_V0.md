# Cora Core v0 contract

## Decision boundary

For each aggregated occurrence, Core receives an Event, fingerprint, occurrence
count, and first-occurrence flag. It returns:

- `decision`: `attention`, `observe`, or `ignore`.
- `root_cause_key`: deterministic cause identity independent of node and service.
- `category`, `rule_id`, `reason`, and `source`.
- optional `experience_version` and `trace_role`.
- `decided_at`.

The JSON contracts live under `schemas/`.

## Safe defaults

- No Pack for a product line: `observe` from `framework_default`.
- No matching rule in a loaded Pack: the Pack's declared default, normally `observe`.
- Core error or invalid decision: fail open to `observe`; occurrence facts still persist.
- Product-line Packs never match events from another product line.

## Identity

The stored Problem identity is
`product_line + service + fingerprint + root_cause_key`. Node occurrences and
trend points retain the same cause key. This prevents one generic wrapper
fingerprint from collapsing unrelated causes while still allowing read-only
incident grouping.

## Pack boundary

Packs are external JSON files loaded from `core.experience_pack_dir` or
`-experience-pack-dir`. Public builds do not embed a production Pack. Pack
activation requires review, a version, a SHA-256 in the private model manifest,
and shadow evaluation against private labeled data.
