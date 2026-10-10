# slither — Usage

`slither` is a cheap-model repo scout. It walks a repository, gathers bounded
per-file evidence, optionally scores files with a cheap LLM (via
`github.com/garyblankenship/wormhole`), and writes a Markdown or JSON report.
With no model configured it uses a deterministic fallback score, so the CLI is
useful fully offline.

## Build

```bash
go build -o slither ./cmd/slither
# or run without building:
go run ./cmd/slither report /path/to/repo
```

## Command

Commands are `report`, `version`, `doctor`, `outputs`, `completion`, `agent`,
and `eval`. Every command supports `--help` and `-h`; `slither help COMMAND`
prints the same command-specific reference.

```
slither report [repo] [flags]
slither version [--build|--json]
slither doctor [--json]
slither outputs [surface] [--json]
slither completion bash|zsh
slither agent [repo] [--outcomes path]
slither eval --outcomes path --report path [--report path...] (--json|--markdown) [--out -]
```

`repo` defaults to the current directory (`.`).

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--out` | `slither-report.md` | Output path; `-` writes to stdout. Switches to `slither-report.json` automatically when `--json` is set and `--out` is left at its default. |
| `--top` | `80` | Target number of ranked production files. Separated documentation, test, generated, and completeness rows may make the JSON evidence set larger. Must be positive. |
| `--max-bytes` | `500000` | Maximum bytes inspected per file. Must be positive. |
| `--days` | `90` | History window (days) for churn and bug-fix signals. Must be positive. |
| `--patterns` | (embedded) | Path to a JSON path/content pattern file. Overrides the embedded `premium-model-triage` catalog. |
| `--focus` | (none) | Case-insensitive regexp matched against path, evidence layers, reasons, risk fields, and summary after evidence is computed. |
| `--include` | (none) | Path glob to include before inspection. Repeat or comma-separate values. Supports common `**` forms such as `internal/**` and `**/*_test.go`. |
| `--exclude` | (none) | Path glob to exclude before inspection. Repeat or comma-separate values. |
| `--why-top` | `0` | Add concise explanations for the top N ranked production files in Markdown and JSON. |
| `--inventory` | (none) | Group one canonical review lane: `cli-ux`, `api-contracts`, `data-integrity`, `error-handling`, `dependency-policy`, `security`, `lifecycle-concurrency`, `performance`, `test-risk`, `coupling`, or `architecture`. |
| `--model` | (none) | Cheap model ID for wormhole scoring. Omit for deterministic fallback. |
| `--base-url` | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL. |
| `--api-key-env` | `OPENROUTER_API_KEY` | Environment variable holding the API key. |
| `--local` | `false` | Use the local model profile (see below). |
| `--json` | `false` | Emit a machine-readable JSON evidence envelope. |
| `--summary` | `false` | Emit a concise Markdown summary; with `--json`, emits `slither.summary/v1`. When `--out` is omitted, defaults become `slither-summary.md` or `slither-summary.json`; an explicit `--out slither-report.md` is preserved. |
| `--cull` | `false` | Append a cheap-model cull ledger over reported rows. |
| `--no-cache` | `false` | Disable the content-hash score cache (always re-score). |

## Examples

Deterministic offline report (no model):

```bash
go run ./cmd/slither report /path/to/repo --out slither-report.md --top 80 --days 90
```

Machine-readable evidence envelope:

```bash
go run ./cmd/slither report /path/to/repo --json --out slither-report.json
```

Focus on PostgreSQL, pgx, psql, and migration evidence while excluding tests:

```bash
go run ./cmd/slither report /path/to/repo \
  --focus "postgres|pgx|psql|migration" \
  --exclude "**/*_test.go" \
  --why-top 10
```

Generate a data-integrity lane inventory:

```bash
go run ./cmd/slither report /path/to/repo --inventory data-integrity --json --out slither-data.json
```

Generate a compact handoff with ranking and scoring health:

```bash
go run ./cmd/slither report /path/to/repo --summary --json --out slither-summary.json
```

Append an auditable cull ledger (kept targets, alternates, culled buckets,
evidence intersections, skipped signals):

```bash
go run ./cmd/slither report /path/to/repo --top 80 --cull --json --out slither-cull.json
```

Override the embedded pattern catalog (testing/overriding only):

```bash
go run ./cmd/slither report /path/to/repo \
  --patterns /path/to/triage_patterns.json \
  --json --out slither-report.json
```

Score with OpenRouter via wormhole:

```bash
OPENROUTER_API_KEY=... go run ./cmd/slither report /path/to/repo \
  --model z-ai/glm-5.2 \
  --base-url https://openrouter.ai/api/v1 \
  --out slither-report.md
```

Score with a local OpenAI-compatible server:

```bash
go run ./cmd/slither report /path/to/repo --local --out slither-report.md
```

`--local` uses the `local` profile from the config file — by default model
`Qwen3.6-35B-A3B-oQ4-fp16-mtp`, base URL `http://127.0.0.1:8000/v1`, and API key
env var `SLITHER_API_KEY` — unless you override each explicitly.

## Configuration file

On first `report` run, `slither` writes `~/.config/slither/config.json` (on macOS:
`~/Library/Application Support/slither/config.json`) with built-in defaults, then
reads it on every run. It lets you set a default scoring model without passing
flags or editing source:

```json
{
  "model": "",
  "base_url": "https://openrouter.ai/api/v1",
  "api_key_env": "OPENROUTER_API_KEY",
  "local": {
    "model": "Qwen3.6-35B-A3B-oQ4-fp16-mtp",
    "base_url": "http://127.0.0.1:8000/v1",
    "api_key_env": "SLITHER_API_KEY"
  },
  "fallback_models": []
}
```

- **Precedence:** an explicit CLI flag overrides the config value, which overrides
  the built-in default. `"model": ""` keeps the deterministic offline default; set
  it to a model ID (e.g. `z-ai/glm-5.2`) to make scoring the default.
- **`fallback_models`:** ordered backup model IDs; if the primary is rate-limited
  or over quota, wormhole fails over to the next. Ignored under `--local`.
- **Score cache:** model scores are cached at `~/.config/slither/cache/scores.json`,
  keyed by file evidence + model, so re-runs skip unchanged files. Pass `--no-cache`
  to disable. A missing or corrupt cache is ignored, never fatal. New cache and
  report files are owner-readable only (`0600`); replacing an existing file never
  broadens stricter permissions.

## Agent bridge

Run one JSON object per line through the offline, dependency-free stdio bridge:

```bash
printf '%s\n' '{"schema":"slither.agent/v1","id":"hello-1","op":"hello"}' |
  go run ./cmd/slither agent /path/to/repo
```

`agent` does not load or create user configuration, call a model or network, or
read/write the score cache. It uses embedded patterns and normal scan defaults.
Requests are processed serially and retain at most one in-memory snapshot.

Every request is a single JSON object with the `slither.agent/v1` schema,
printable-ASCII `id` (1–128 bytes), and an operation. Unknown and unused fields
are rejected. LF, CRLF, and a final record ending at EOF are accepted. The
payload limit is 1 MiB (1,048,576 bytes), excluding its line ending. An oversized
line is discarded through its newline and returns `request_too_large`, so the
following request can proceed.

```json
{"schema":"slither.agent/v1","id":"request-1","op":"hello"}
```

Successful responses contain `schema`, `id`, `ok:true`, and `result`.
Snapshot-backed successes also contain `report_id` and `source_state`. Error
responses contain only the protocol envelope and a code, never paths, raw
errors, provider output, source, or snippets:

```json
{"schema":"slither.agent/v1","id":"request-1","ok":false,"error":{"code":"invalid_request"}}
```

### Hello and operation fields

`hello` accepts no operation fields. Its result has fixed values; the
`schemas` and `operations` arrays have the following fixed ordering:

```json
{"protocol":"slither.agent/v1","schemas":["slither.report/v1","slither.context/v1","slither.outcome/v1","slither.eval/v1"],"operations":["hello","scan","query","context","feedback"],"max_request_bytes":1048576,"max_context_bytes":1048576,"feedback_enabled":false}
```

Pass `--outcomes <path>` to make `feedback_enabled` true. This authorizes
only the named ledger file; its parent directory must already exist.

| Operation | Allowed fields beyond `schema`, `id`, `op` | Result |
| --- | --- | --- |
| `hello` | none | Fixed protocol/schema/operation lists, both 1 MiB limits, and feedback state. |
| `scan` | none | Forces a rebuild and returns report parameters, discovery, skipped signals, all eight cull counts, queues, and lanes. |
| `query` | required `limit` (1–80); optional `target_ids`, `focus` | Bounded summaries with `count` and `truncated`; IDs and regexp focus form a deduplicated union in report-row order. |
| `context` | required `budget_bytes`; optional `target_ids`, `focus` | A redacted, byte-bounded `slither.context/v1` packet. Maximum budget: 1 MiB. |
| `feedback` | required `report_id`, `evidence_id`, `verdict`, `files_opened`, `tool_calls`, `review_ms` | A derived outcome record; success is `{"recorded":true}`. |

Logical target IDs have exactly the form `slither:file:<base64>`, where
`<base64>` is unpadded URL-safe base64 for the slash-separated relative path.
For example, `auth.go` is `slither:file:YXV0aC5nbw`. Report and evidence IDs
are lowercase `sha256:` identities.

`query` summaries include rank, logical/evidence IDs, path, score provenance,
class, confidence, actionability, caveat, cull disposition, verification command,
and at most eight evidence layers and reasons. They never include source,
excerpts, snippets, or model prose. `context` retains its selection, redaction,
proof-obligation, omission, ordering, containment, and source-read contracts.

Filesystem-discovered repositories rebuild before every non-`hello` operation;
`scan` always rebuilds. A clean Git snapshot is reused only while `HEAD` and
clean status are unchanged. Dirty/untracked state fingerprints status plus each
regular file's bounded inspected prefix, while Git metadata failure forces a
rebuild. A failed refresh clears the prior snapshot rather than treating it as
current.

Stable errors are `invalid_request`, `unsupported_schema`, `unsupported_op`,
`request_too_large`, `budget_too_small`, `budget_too_large`,
`target_not_found`, `stale_evidence`, `feedback_disabled`,
`feedback_write_failed`, `scan_failed`, and `canceled`.

## Outcome ledger

Feedback is opt-in: without `--outcomes`, it returns `feedback_disabled`.
Slither validates supplied report and evidence IDs against the active snapshot,
then derives rank, score, and lane rather than accepting client classification.
`verdict` is `confirmed`, `refuted`, `unknown`, or `skipped`; the three
counters are non-negative integers.

The JSONL record is `slither.outcome/v1` and contains only these typed fields:

```json
{"schema":"slither.outcome/v1","timestamp":"2024-01-02T03:04:05Z","report_id":"sha256:…","evidence_id":"sha256:…","lane":"kept_for_premium","rank":1,"score":4,"verdict":"confirmed","files_opened":2,"tool_calls":3,"review_ms":125}
```

It intentionally stores no repository path, file path, source, snippet, prompt,
command, or arbitrary feedback prose. Encoded JSON plus newline must not exceed
1 MiB. A ledger is a path-bound append-only regular file: a new file is created
`0600`; an existing file must already be owner-readable and owner-writable with
no group/other bits. Symlinks, directories, devices, sockets, and replacements
after startup are rejected. Each append rechecks the parent and target, writes
one JSON object plus newline, syncs, and closes.

## Evaluation

Evaluate one or more report JSON files against the ledger:

```bash
go run ./cmd/slither eval \
  --outcomes ./slither-outcomes.jsonl \
  --report ./slither-report.json \
  --report ./earlier-report.json \
  --json --out -
```

`--outcomes` is required exactly once; one or more repeatable `--report` flags
and exactly one of `--json` or `--markdown` are required; positional arguments are rejected. `--out` defaults
to `-`; a file output is atomic and `0600`, and cannot alias the ledger or any
report input. Evaluation does not load configuration, scan a repository, call a
model or network, write a cache, or modify its inputs.

Each report must be `slither.report/v1` with a valid identity, ordered unique
evidence IDs, valid score provenance, and an identity that matches its contents.
Duplicate report identities are rejected. Matched outcomes must revalidate to
the report's one-based rank, score, and derived cull lane.

The ledger streams in order with a 1 MiB per-record cap; blank lines are ignored.
Malformed or oversized interior records are fatal. Exactly one incomplete final
JSON record without a newline is ignored and increments
`warnings.partial_trailing_records`; a complete but invalid final record is
fatal. Complete records outside the supplied report/evidence set are excluded
and increment `warnings.unmatched_outcomes`.

The deterministic `slither.eval/v1` result has report/row counts, matched
outcome and context-cost totals, calibration, and warning counts. It contains no
timestamps, repository paths, report IDs, or input paths.

### Evaluation formulas

`top_k` is fixed at 15. `confirmed` and `refuted` are labels; `unknown`
and `skipped` contribute to outcome and context-cost totals but not labels. Only
matched, integrity-valid records contribute to matched totals.

Let `slots = sum(min(15, rows in each report))`. Let `confirmed_total` be
matched confirmed outcomes plus unmatched confirmed outcomes.

| Field | Exact value |
| --- | --- |
| `found` | Matched labeled (`confirmed` or `refuted`) outcomes. |
| `missing` | Unmatched labeled outcomes. |
| `noise_in_top_k` | Matched refuted outcomes with revalidated `rank <= 15`. |
| `confirmed_in_top_k` | Matched confirmed outcomes with revalidated `rank <= 15`. |
| `distinct_scores_in_top_k` | Distinct scores among each report's first 15 rows, summed per report. |
| `noise_top_k_rate` | `noise_in_top_k / slots`. |
| `confirmed_top_k_recall` | `confirmed_in_top_k / confirmed_total`. |
| `confirmed_found_recall` | Matched confirmed outcomes / `confirmed_total`. |
| `labeled_coverage` | `found / labeled`. |
| `top_k_score_saturation` | `1 - (distinct_scores_in_top_k / slots)`. |

Every ratio with a zero denominator is `0`; therefore
`top_k_score_saturation` is `1` when `slots` is zero. Unchanged inputs render
byte-identical JSON plus one newline. `--markdown` renders the same totals,
formula `slots` denominator, context cost, Top-K health, recall/coverage, and ledger warnings
without paths, IDs, timestamps, or input names.

## Output

The Markdown report leads with **Executive Triage** (confidence breakdown,
review lanes, and a start-here pointer), then **Ranked Files** (a compact table
of the top production files with confidence, actionability, evidence, review
command, key signals, and a note), and finally **Detailed Signals** (per-file
seed score, class, actionability, churn with its post-creation component, and
risk fields). Generated,
documentation, and test/fixture files are omitted from the ranked queue and
appear in separate **Documentation Rows** and **Test Risk Rows** sections when
present; `--json` retains the full evidence set. Discovery counts, the pattern
source, and skipped signals are included so missing evidence is visible rather
than treated as low risk.

When an output file already exists, the next report includes a freshness hint if
that previous output was older than the newest scanned file before the current
run rewrote it.

### Churn decomposition

Raw `churn` counts numstat lines added plus deleted over the history window,
which mixes two unrelated shapes: files born large that barely changed since,
and files with a problem spot that keeps changing. Every row therefore also
carries:

| Field | Meaning |
| --- | --- |
| `commit_touches` | Commits that touched the file inside the window. |
| `churn_after_creation` | Churn excluding the file's creation commit. When the creation predates the window or cannot be identified (renames), this equals raw churn — the conservative direction for review pressure. |
| `churn_profile` reason | `churn_profile:creation-dominated` (churn is mostly size), `churn_profile:reworked` (a small number of large post-creation rewrites), `churn_profile:recurring-fixes` (repeated bug-fix visits — the problem-spot shape), or `churn_profile:evolving` (steady post-creation change). Single-touch files stay `stable` and emit no profile reason. |

Read the `post-create` column (Markdown) or `churn_after_creation` (JSON) —
not raw churn — as change pressure, and read it beside `fix_touches`: a large
file created once and a small file fixed five times can share a churn number
while meaning opposite things for review priority. Scoring agrees: every
pressure gate and the seed score's churn component consume post-creation churn
only.

### Actionability

Each evidence row carries an `actionability` value in Markdown and JSON:

| Value | Meaning |
| --- | --- |
| `likely_defect` | Start here when a concrete defect-shaped detector such as SSRF, CSRF, IDOR, traversal, unsafe parsing, or credential literal evidence is corroborated by another evidence layer. |
| `high_risk_inspect` | Inspect high-risk corroborated evidence such as migration, workflow, infrastructure, OpenAPI, CORS, cookie, stale-marker, flaky-test, or oracle signals. This is risk triage, not a defect claim. |
| `inspect` | Read the file as a strong review seed. The row has enough evidence to justify premium review, but Slither is not claiming a defect. |
| `dependency_review` | Review dependency manifests and replacement policy separately from defect triage. |
| `hotspot` | Review when you care about blast radius, churn, centrality, ownership, or code smell. Hotspot rows are prioritization evidence, not bug claims. |
| `verify_first` | Check context before spending premium review. This covers generated/docs/test-only rows, detector fixtures, weak lexical evidence, model errors, and low-signal rows. |

`actionability` is deterministic and derived from the row evidence. When
`--cull` is enabled, kept and alternate rows retain that intrinsic value, while
culled rows render as `verify_first`; the explicit `cull_decision` remains
available for bucket filtering. Summary actionability counts use the same
rendered view as the full report.
Candidate verification commands use POSIX-shell quoting for repository-controlled
arguments. When a path contains a carriage return or newline, Slither omits any
candidate command that would need to embed that path because no portable, exact
one-line shell representation exists; path-independent package commands may remain.

### JSON envelope (`--json`)

With `--json` the report is emitted as a single JSON object instead of
Markdown. Top-level fields:

| Field | Type | Description |
| --- | --- | --- |
| `run_label` | string | Constant `"slither_report"` identifying the payload. |
| `repo` | string | Scanned repository path. |
| `generated_at` | string (RFC 3339) | Report generation timestamp. |
| `days` | int | Day window applied to discovery. |
| `patterns_source` | string | Source of the scoring patterns (embedded catalog, or the `--patterns` file). |
| `files_seen` | int | Files discovered before scoring. |
| `files_reported` | int | Number of files included in `rows`. |
| `discovery` | object | Discovery audit: `source`, `git_tracked`, `git_untracked`, `filesystem_files`, `candidate_files`. |
| `model` | string | Model ID used (omitted when empty). |
| `base_url` | string | Model base URL (omitted when empty). |
| `skipped_signals` | string[] | Signals skipped during scanning (omitted when empty). |
| `filters` | object | Active filter metadata: `focus`, `include`, `exclude`, and `inventory` when set. |
| `rows` | object[] | Per-file evidence. Each row carries `id`, `path`, `evidence_class`, `confidence`, `actionability`, `score`, `reasons`, `summary`, plus per-file risk and count fields. When `--cull` is enabled, each row also carries `cull_decision` and `cull_reason`, using the same bucket names as the cull ledger. |
| `why_top` | object[] | Concise top-ranked explanations when `--why-top N` is set. Each entry has `rank`, `path`, `score`, `confidence`, `actionability`, `evidence`, `reasons`, `verify_cmd`, and `note`. |
| `freshness_hint` | string | Present when an existing output file was compared to current scanned files before being rewritten. |
| `first_read_queue` | object[] | Files to read first; each entry has `id`, `group`, `lane`, `confidence`, `reasons`, `files`, `caveat` (omitted when empty). |
| `review_plan` | object[] | Review lanes; each has `id`, `lane`, `group`, `files`, `gates`, `verify`, `why`, `confidence`, `caveat` (omitted when empty). |
| `cull_ledger` | object | Cull ledger, present when culling is enabled via `--cull`: which files were kept, demoted to alternates, or culled, with bucketed reasons and `actionability` on examples (omitted otherwise). |

### Summary ranking health (`--summary`)

`slither.summary/v1` carries a `ranking_health` object. Its top-k fields
(`slots`, `distinct_top_k_scores`, `saturation`) use the same definition as
eval's `top_k_score_saturation`: the first 15 rows of the report's full
`rows` array in report order — including the separated test/fixture and
documentation rows that the Markdown ranked queue omits. A repository whose
top band is dominated by test files can therefore show near-total saturation
(`distinct_top_k_scores` of 1) while the ranked production queue — the
"Start Here" list — still discriminates across several scores. The Markdown
summary labels the line with this basis for the same reason.

## Scan behavior

These limits and heuristics are fixed in the scanner (not flags):

| Behavior | Value |
| --- | --- |
| File discovery | `git ls-files` when the repo is a Git checkout; otherwise a filesystem walk. |
| Skipped directories | `.git`, `node_modules`, `vendor`, `dist`, `build`, `target`, `coverage`, `.next`, `.svelte-kit`, `.venv`, `.work` |
| Skipped file suffixes | `.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.pdf`, `.zip`, `.gz`, `.tar`, `.mp4`, `.mp3`, `.lock`, `.sum` |
| Binary detection | A file is treated as binary (and skipped) if a NUL byte appears in its first 4096 bytes. |
| Excerpt length | Per-file summaries are truncated to 180 characters with a trailing `...`. |
| Test-gap signal | Non-test source files of 80 or more lines are flagged with a `test-gap` reason. |
| Size signal | Files of 300 or more lines get a `size:<lines> lines` reason. |

## Security

Do not commit API keys or generated reports containing private repository
data. Pass credentials through the configured `--api-key-env` variable.
