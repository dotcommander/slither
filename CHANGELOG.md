# Changelog

## Unreleased

- Three hygiene fixes from the second reliquary triage pass. Documentation-
  only files (≤3 code lines after comment stripping, ≥10 non-blank lines) no
  longer score content/unknowns patterns — observed `vector/doc.go`, a pure
  package doc, ranked #9 of 68 production files on prose words like
  "helpers" and "recall" (`custom_infra_reinvention:4`) — and carry an
  explicit `doc_only:content_patterns_skipped` marker. Files under an
  `examples`/`example` directory segment no longer emit
  `test_gap:no nearby test` (quickstarts are executable documentation;
  the user-surface lane owns their review). Discovery now excludes the
  tool's own report outputs (`slither-report.*`, `slither-summary.*`,
  `slither-cull*`), so rescanning a repo after writing a summary no longer
  feeds slither's artifacts back in as evidence rows.
- Recalibrated `async_messaging_boundary` after a Go capitalization
  collision: exported struct fields like the retrieval eval `Topic` matched
  the bare capitalized `Queue|Topic|Consumer|Producer|Subscribe|Publish`
  alternatives (14 files flagged, including an offline tuner scored as a
  messaging boundary at weight 3). Brand names (Kafka, RabbitMQ, SQS, …)
  still match anywhere; the generic words now require call shape
  (`Publish(`, `Subscribe(`), since Go capitalizes every exported identifier.
- Recalibrated test-flake detection on the same evidence: deterministically
  seeded sources (`rand.New(rand.NewSource(42))`, the responsible pattern)
  and in-process `httptest` servers no longer count as nondeterminism —
  only global rand calls (auto-seeded since Go 1.20), time-seeded sources,
  live `time.Now`, and real network calls do; `time.Sleep` in a file that
  imports `testing/synctest` is treated as the fake clock it is (observed:
  seven 30–61-minute virtual waits in memory tests flagged as fixed waits).
  `offset_pagination` no longer matches case-insensitively, so prose about
  character offsets stays out of SQL-pagination risk, and `isTestFile` now
  recognizes `*_test.sh/.bash/.zsh` so test scripts get the test caveat and
  flake/oracle gating instead of production ranking.
- Hardened two attribution heuristics found while triaging reliquary. Bug-fix
  subjects now match on word boundaries (fix/bug/regression/crash/panic/broken
  plus bugfix/hotfix and common inflections), so subjects like "add nomic task
  *prefixes*" no longer inflate `fix_touches`; matching is also subject-only,
  as documented. Go package imports in packages without a `pkg.go`-style owner
  file are still attributed to the alphabetically first file, but the reason
  is now labeled `centrality:package_refs:N` instead of
  `centrality:incoming_refs:N` so package fan-in cannot be misread as resolved
  file-level dependents; scoring is unchanged.
- Decomposed churn so review pressure separates from file size: rows now
  carry `commit_touches`, `churn_after_creation` (churn excluding the creation
  commit, identified from full-history add records), and a derived
  `churn_profile` (`creation-dominated`, `reworked`, `recurring-fixes`,
  `evolving`, `stable`) emitted as a `churn_profile:*` reason. Every pressure
  gate and the seed score's churn component now consume post-creation churn at
  the unchanged floor of 120 touched lines, so a file born large no longer
  outranks a repeatedly fixed one. The Markdown detail table gained a
  `post-create` column; unidentified creations (renames, pre-window history)
  conservatively count all churn as pressure. Calibration fixture population
  digests were refreshed for the additive row fields; recorded labels and
  verdicts are unchanged.

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
