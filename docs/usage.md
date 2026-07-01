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

There is one command: `report`.

```
slither report [repo] [flags]
```

`repo` defaults to the current directory (`.`).

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--out` | `slither-report.md` | Output path; `-` writes to stdout. Switches to `slither-report.json` automatically when `--json` is set and `--out` is left at its default. |
| `--top` | `80` | Maximum rows to include in the report. Must be positive. |
| `--max-bytes` | `500000` | Maximum bytes inspected per file. Must be positive. |
| `--days` | `90` | History window (days) for churn and bug-fix signals. Must be positive. |
| `--patterns` | (embedded) | Path to a JSON path/content pattern file. Overrides the embedded `premium-model-triage` catalog. |
| `--focus` | (none) | Case-insensitive regexp matched against path, evidence layers, reasons, risk fields, and summary after evidence is computed. |
| `--include` | (none) | Path glob to include before inspection. Repeat or comma-separate values. Supports common `**` forms such as `internal/**` and `**/*_test.go`. |
| `--exclude` | (none) | Path glob to exclude before inspection. Repeat or comma-separate values. |
| `--why-top` | `0` | Add concise explanations for the top N ranked production files in Markdown and JSON. |
| `--inventory` | (none) | Group a review-lane inventory instead of a general risk queue. Currently supports `data-integrity`. |
| `--model` | (none) | Cheap model ID for wormhole scoring. Omit for deterministic fallback. |
| `--base-url` | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL. |
| `--api-key-env` | `OPENROUTER_API_KEY` | Environment variable holding the API key. |
| `--local` | `false` | Use the local model profile (see below). |
| `--json` | `false` | Emit a machine-readable JSON evidence envelope. |
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

On first run `slither` writes `~/.config/slither/config.json` (on macOS:
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
  to disable. A missing or corrupt cache is ignored, never fatal.

## Output

The Markdown report leads with **Executive Triage** (confidence breakdown,
review lanes, and a start-here pointer), then **Ranked Files** (a compact table
of the top production files with confidence, actionability, evidence, review
command, key signals, and a note), and finally **Detailed Signals** (per-file
seed score, class, actionability, churn, and risk fields). Generated,
documentation, and test/fixture files are omitted from the ranked queue and
appear in separate **Documentation Rows** and **Test Risk Rows** sections when
present; `--json` retains the full evidence set. Discovery counts, the pattern
source, and skipped signals are included so missing evidence is visible rather
than treated as low risk.

When an output file already exists, the next report includes a freshness hint if
that previous output was older than the newest scanned file before the current
run rewrote it.

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

`actionability` is deterministic and derived from the row evidence. It is
separate from `cull_decision`: culling decides which bucket a row belongs in;
actionability describes how a reviewer should treat that row inside any bucket.

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
