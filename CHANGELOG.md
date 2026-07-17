# Changelog

## v0.1.0-rc1 - 2026-07-17

### Added

- External product-line Pack loading through Server CLI and YAML configuration.
- Root-cause-aware Problem identity and trace projection evidence.
- Generic single-service and multi-service Agent examples.
- Fictional Pack/model examples, configuration guide, and upgrade guide.

### Changed

- Public builds now start in observe-only mode unless a private Pack directory is
  configured explicitly.
- Public documentation now describes reusable product behavior instead of one
  deployment's topology and operational history.
- `cora-eval` requires an explicit Pack directory and product line.
- `cora-iterate` requires an explicit private Pack manifest.

### Removed

- Product-specific Packs, production-shaped configurations, real log fixtures,
  evaluation reports, and historical handoffs from the public worktree.

### Upgrade note

Deployments upgrading from an embedded-Pack build must configure
`core.experience_pack_dir`. See `docs/UPGRADING.md`.
