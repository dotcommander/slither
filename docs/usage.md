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
| `--model` | (none) | Cheap model ID for wormhole scoring. Omit for deterministic fallback. |
| `--base-url` | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL. |
| `--api-key-env` | `OPENROUTER_API_KEY` | Environment variable holding the API key. |
| `--local` | `false` | Use the local model profile (see below). |
| `--json` | `false` | Emit a machine-readable JSON evidence envelope. |
| `--cull` | `false` | Append a cheap-model cull ledger over reported rows. |

## Examples

Deterministic offline report (no model):

```bash
go run ./cmd/slither report /path/to/repo --out slither-report.md --top 80 --days 90
```

Machine-readable evidence envelope:

```bash
go run ./cmd/slither report /path/to/repo --json --out slither-report.json
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

`--local` sets the model to `Qwen3.6-35B-A3B-oQ4-fp16-mtp`, the base URL to
`http://127.0.0.1:8000/v1`, and the API key env var to `SLITHER_API_KEY`
unless you override each explicitly.

## Output

The Markdown report leads with **Executive Triage** (confidence breakdown,
review lanes, and a start-here pointer), then **Ranked Files** (a compact table
of the top production files with confidence, evidence, review command, key
signals, and a note), and finally **Detailed Signals** (per-file seed score,
class, churn, and risk fields). Generated, documentation, and test/fixture
files are omitted from the ranked queue and appear in separate **Documentation
Rows** and **Test Risk Rows** sections when present; `--json` retains the full
evidence set. Discovery counts, the pattern source, and skipped signals are
included so missing evidence is visible rather than treated as low risk.

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
| `rows` | object[] | Per-file evidence. Each row carries `id`, `path`, `evidence_class`, `confidence`, `score`, `reasons`, `summary`, plus per-file risk and count fields. |
| `first_read_queue` | object[] | Files to read first; each entry has `id`, `group`, `lane`, `confidence`, `reasons`, `files`, `caveat` (omitted when empty). |
| `review_plan` | object[] | Review lanes; each has `id`, `lane`, `group`, `files`, `gates`, `verify`, `why`, `confidence`, `caveat` (omitted when empty). |
| `cull_ledger` | object | Cull ledger, present when culling is enabled via `--cull`: which files were kept, demoted to alternates, or culled, with bucketed reasons (omitted otherwise). |

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
