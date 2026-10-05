# Deterministic score contract

Slither ranks rows by the public qualitative score first. The continuous
`seed_score` remains a secondary ordering signal and is not the public tier.
Model scoring, when configured, is a separate selected score with explicit
provenance.

The deterministic qualitative score is still the established bounded mapping:
take the strongest raw risk component, divide by three with upward rounding,
add one point when at least two evidence layers are present, then clamp the
result to the inclusive range 1–5.

## Churn semantics

Churn is numstat lines added plus deleted over the `--days` window. Because a
file born large and a file with a repeatedly fixed problem spot can share a
churn number, the scorer decomposes it before use:

- The file's creation commit is identified from full-history `--diff-filter=A`
  records. Its churn stays in the reported total but never counts as pressure.
- `commit_touches` counts commits touching the file inside the window.
- Every pressure gate (`env_contract`, `cochange`, `ownership`, `stale_marker`,
  `hotspot`) and the seed score's churn component consume post-creation churn
  at the unchanged floor of 120 touched lines, not raw churn.
- The derived `churn_profile` (`creation-dominated`, `reworked`,
  `recurring-fixes`, `evolving`, `stable`) labels the shape for humans and is
  emitted as a `churn_profile:*` reason; it adds no new evidence layer beyond
  the existing churn layer.

When creation cannot be identified — renamed files, or creations older than
available history — all churn counts as post-creation, which overstates
pressure rather than hiding it. Bug-fix identification matches fix/bug/
regression/crash/panic/broken (plus bugfix/hotfix and common inflections) on
**word boundaries in the commit subject** — "prefixes", "suffix", and
"fixtures" never count — with the unchanged minimum of 30 commits in the
window.

## Centrality attribution

Go imports name packages, not files, so each local package import is attributed
to at most two owner files: the package's `pkg.go`-style name, `types.go`,
`interfaces.go`, or similar. When a package has no such file, the
alphabetically first file absorbs the package's fan-in and the reason is
labeled `centrality:package_refs:N` instead of `centrality:incoming_refs:N`:
the count is real package-level fan-in, but the file was not resolved as the
hub — read it as "this package is heavily imported," not "this file has N
direct dependents." The score contribution is unchanged; only the label
protects the reading.

## 2026-07-29 calibration decision

A fresh calibration run pinned `2026-07-29T16:00:00Z` as its single `as_of`
time, froze current public Slither, Repomap, and Reliquary sources, and kept a
clean private repository as an aggregate-only holdout. Repomap, Reliquary, and
the holdout were scanned from clean full-history Git clones at their recorded
commits; a source-only archive is not an acceptable calibration source. Slither
used the prerequisite dirty Git snapshot but excluded the derived calibration
fixture from its own cohort, making the recorded tree and report identities
reproducible after the fixture is replaced. The checked-in fixture contains
public relative paths, score inputs, review
verdicts, rationales, verification commands, case content identities, and exact
Git/source/report provenance. It contains no source excerpts, absolute paths,
or private holdout paths.

The initial 40 cases per public repository were selected from the Top 15,
distributed ranks 16–80, strongest direct-risk rows, and deterministic
SHA-256(path) low-score samples. The evidence expansion added the next 20
strongest-risk rows per repository.

Labels were carried forward only when repository, relative path, and
`content_id` matched. Fifty-four Reliquary labels carried forward and six cases
were newly selected. Independent review covered every newly bound Slither and
Reliquary case, including 19 Slither cases rebound across the final
public-readiness fixes and lifecycle closure. The final cohort records nine
confirmed findings across two repositories and 143 refuted findings, passing
the unchanged gate of at least six confirmed findings across two repositories
and at least 24 refuted findings.

All 2,002 fixed threshold and corroboration candidates were evaluated. Sixty-
seven preserved the reviewed metrics before cull-lane checks; 15 also kept
every confirmed production row in a kept or alternate review lane. A different
set of five no-bonus mappings passed the saturation limits. The exact
intersection between saturation-eligible and reviewed-quality-eligible mappings
was empty.

Consequently this calibration retains the current score mapping. The model
score cache contract, report schema, CLI flags, and deterministic identity
contract are unchanged. Reports and outcome ledgers continue to match only
their own recorded report and evidence identities.

## Final aggregate QA

The final no-model, no-cache reports used the frozen public snapshots and the
clean aggregate-only private holdout. Distribution columns are scores 1–5.
The fixture gives every row a content-derived aggregate identity. It retains
the exact report identity for the non-self-referential snapshots; Slither uses
exact frozen dirty-tree and report provenance, while its final-QA entry leaves
the report ID empty and records the post-fixture aggregate identity.

| source | distribution | Top-15 saturation | score-5 share | kept / alternate | confirmed Top-15 recall | refuted in Top 15 | changed common ranks |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Slither | 23 / 16 / 22 / 13 / 23 | 0.933 | 0.237 | 15 / 4 | no confirmed labels | 15 | 0 / 97 |
| Repomap | 58 / 46 / 85 / 52 / 62 | 0.933 | 0.205 | 24 / 49 | 0.500 | 1 | 0 / 303 |
| Reliquary | 74 / 94 / 87 / 37 / 29 | 0.933 | 0.090 | 24 / 33 | 0.286 | 13 | 0 / 321 |
| private holdout | 159 / 261 / 223 / 182 / 233 | 0.933 | 0.220 | 24 / 304 | unlabeled | unlabeled | 0 / 1058 |

All four final aggregate reports preserve their prerequisite row order and
Top-15 membership.

The five saturation-eligible mappings were `[18,24,48,60]`,
`[18,30,48,60]`, `[24,30,48,60]`, `[24,36,48,60]`, and
`[30,36,48,60]`, all without a corroboration bonus. All five failed the
reviewed quality metrics. None intersected the 15 mappings that also preserved
every confirmed production row’s kept-or-alternate review lane.
