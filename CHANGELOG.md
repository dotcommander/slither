# Changelog

## v0.3.0 (2026-07-30)

- Made command help position-independent without stealing flag values, scrubbed
  credentials and URL userinfo from report and model-prose surfaces, and kept
  oversized score caches deterministically bounded even when one run touches
  more than 5,000 entries. Existing cache entries are scrubbed and rewritten
  when they contain pre-hardening model secrets. Report generation now also
  closes model discovery resources after every configured scoring run.
- Rebuilt calibration from one fixed-time Git-aware cohort, bound reviewed
  cases to content identities, rejected archive-only provenance, excluded the
  derived fixture from its own cohort, and kept the existing qualitative
  scoring contract after screening all 2,002 mappings.
- Confined manifest enrichment to regular in-repository files, corrected
  unknown-command help guidance, and completed `slither help` shell completion.
- Added canonical command help, output discovery (`slither outputs`), and
  dependency-free bash/zsh completion from shared command metadata.
- Added strict `version --json`, read-only `doctor`, compact report summaries,
  all canonical review-lane inventories, and privacy-minimal eval Markdown.

- Added the dependency-free `slither agent` JSONL bridge with bounded,
  repository-bound report, query, context, and optional feedback operations.
- Added opt-in `slither.outcome/v1` ledger persistence with owner-only file
  permissions, replacement checks, and privacy-minimal derived records.
- Added deterministic multi-report `slither eval` output with input-integrity
  validation, partial-tail warnings, and calibration metrics.
- Documented offline behavior, protocol limits, snapshot invalidation, and
  evaluation formulas.
