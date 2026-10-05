# slither

`slither` scans a repository entirely on your machine and ranks its files into a bounded, evidence-carrying review queue, so the first file you open is the one most worth reading. The whole first-use loop below runs offline — no model, no API key, no network.

## Contents

| If you want to… | Go to |
| --- | --- |
| Produce your first ranked report | [First use](#first-use) |
| Pick Markdown, JSON, summary, or cull output | [Capability reference](#capability-reference) |
| Score with a cheap model (OpenRouter, local server, or Jev) | [Model scoring](#model-scoring) |
| Drive an agent or record verdicts | [Agent bridge](#agent-bridge) |
| Measure whether the rankings were right | [Outcome ledger and evaluation](#outcome-ledger-and-evaluation) |
| Check readiness or install shell completion | [Readiness and shell surfaces](#readiness-and-shell-surfaces) |
| Know what slither deliberately will not do | [Non-goals](#non-goals) |
| Verify a change to slither itself | [Verification](#verification) |

## First use

Prerequisite: a Go toolchain on `PATH`. Side effect: the first `report` run creates the slither config file with built-in defaults (see [Configuration and state](#configuration-and-state)); nothing else on your machine is modified except the report file you name.

Install the published release and run a report inside any Git checkout:

```bash
go install github.com/dotcommander/slither/cmd/slither@latest
slither report . --out slither-report.md --top 80 --days 90
```

Expected observable result: the command prints one line

```
slither wrote slither-report.md with N report rows and M ranked files
```

and writes `slither-report.md`, which opens with `# Slither Report`, then an **Executive Triage** block (confidence counts, review lanes, a start-here pointer), a **Ranked Files** table, and separated documentation/test rows.

Why that follows: `slither` discovers files with `git ls-files` (a filesystem walk when there is no Git repository), inspects at most 500,000 bytes per file (`--max-bytes`), and scores every file deterministically — the strongest raw risk component divided by three and rounded up, plus one point when two or more evidence layers agree, clamped to 1–5 ([docs/scoring-contract.md](docs/scoring-contract.md)). With no model configured — the default — scoring never leaves this loop, which is why the run needs no key and no network. An *evidence row* is one file's collected signals: path terms, content-pattern hits, churn, size, and similar layers named in the report.

Next safe variation: `slither report . --summary` for a compact triage view, or `slither doctor` to check configuration and local-model readiness (read-only).

## Configuration and state

On the first `report` run, slither writes its config to the user config directory — on macOS `~/Library/Application Support/slither/config.json`, elsewhere typically `~/.config/slither/config.json` — then reads it on every run. Current defaults:

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
  "jev": {
    "model": "",
    "base_url": "https://api.typesafe.ai/v1/systemone",
    "api_key_env": "TYPESAFE_API_KEY"
  },
  "fallback_models": []
}
```

- Precedence: an explicit CLI flag overrides the config value, which overrides the built-in default. `"model": ""` keeps deterministic offline scoring.
- `fallback_models` are ordered backup model IDs used when the primary is rate-limited or over quota; they do not apply to `--local` or `--jev`.
- Model scores are cached at `slither/cache/scores.json` beside the config, keyed by file evidence plus model, so re-runs skip unchanged files. Cache and report files are written owner-only (`0600`) and atomically; a missing or corrupt cache is ignored, never fatal. Pass `--no-cache` to disable.
- Existing configs missing the newer `jev` key keep the built-in Jev defaults above.

## Non-goals

These limits are deliberate design, stated before the capability list so the tool is not oversold:

- slither ranks review candidates; it does not claim defects. Its `actionability` labels (`likely_defect`, `hotspot`, `verify_first`, …) are triage vocabulary, and hotspot rows are prioritization evidence, not bug claims.
- The `agent` bridge loads no user configuration, calls no model or network, and never touches the score cache.
- `eval` reads its inputs only; it does not scan a repository, call a model, write cache, or modify its inputs.
- `doctor` creates no configuration and contacts loopback endpoints only.
- The outcome ledger stores no repository paths, file paths, source, snippets, prompts, or free-text feedback — only typed verdict records.

## Capability reference

`slither` has seven commands: `report`, `agent`, `eval`, `doctor`, `outputs`, `completion`, and `version`; every command supports `--help` and `-h`, and `slither help COMMAND` prints the same reference. Model-scoring and completion examples below are verified against source only and were not executed against a live endpoint or shell. Full flag tables, the agent JSONL contract, and the exact evaluation formulas live in [docs/usage.md](docs/usage.md); output selection guidance in [docs/output-guide.md](docs/output-guide.md).

### Reports and filters

`slither report [repo] [flags]` — `repo` defaults to `.`. Defaults: `--top 80`, `--days 90`, `--max-bytes 500000`, `--out slither-report.md`.

- `--summary` — concise summary; with `--json` emits `slither.summary/v1`. Output defaults switch to `slither-summary.md`/`slither-summary.json` unless `--out` is set.
- `--json` — machine-readable `slither.report/v1` envelope; the default output becomes `slither-report.json` when `--out` is unset.
- `--cull` (with `--json`) — appends a cull ledger: which rows were kept for premium review, demoted to alternates, or culled (generated, documentation, test-only, low-signal, duplicate-surface), each with bucketed reasons.
- `--focus` / `--include` / `--exclude` — post-scan regexp filter and path-glob filters (repeat or comma-separate; `**` forms supported).
- `--inventory <lane>` — one canonical review-lane grouping (`security`, `data-integrity`, `test-risk`, …).
- `--why-top N` — concise explanations for the top N ranked files.

Example: focus on PostgreSQL evidence while excluding tests:

```bash
slither report . --focus "postgres|pgx|psql|migration" --exclude "**/*_test.go" --why-top 10
```

- `--patterns <file>` overrides the embedded `triage_patterns.json` catalog (report header shows `embedded:triage_patterns.json`). Intended for testing or deliberate override only.

### Model scoring

All three modes score only the top band of rows after deterministic pre-ranking, in batches of 8 with at most 4 concurrent calls and a 90-second cap per call; rows outside the band keep their deterministic score. An unset model in the selected profile keeps deterministic scoring.

Prerequisite: the named API-key environment variable must hold your key, and the endpoint must be reachable.

OpenRouter via the bundled wormhole client (source-checked, not executed):

```bash
OPENROUTER_API_KEY=… slither report . --model z-ai/glm-5.2 --out slither-report.md
```

A local OpenAI-compatible server using the `local` profile above (source-checked, not executed; requires the server at `http://127.0.0.1:8000/v1`):

```bash
slither report . --local --out slither-report.md
```

Jev scoring sends the top band one file at a time through the typed-verdict endpoint in the `jev` profile — a judgment client that returns a structured 1–5 level instead of free prose — rather than wormhole's batch prompt (source-checked, not executed):

```bash
TYPESAFE_API_KEY=… slither report . --jev --out slither-report.md
```

`--jev` and `--local` are mutually exclusive. The published v0.3.0 release installed by `go install …@latest` predates `--jev`; build this checkout (see [Verification](#verification)) to use it.

### Agent bridge

`slither agent [repo] [--outcomes path]` reads one JSON object per line on standard input and answers in `slither.agent/v1` JSONL. Requests are processed serially; each line is capped at 1 MiB.

```bash
printf '%s\n' '{"schema":"slither.agent/v1","id":"hello-1","op":"hello"}' |
  slither agent /path/to/repo
```

Expected result (observed output):

```json
{"schema":"slither.agent/v1","id":"hello-1","ok":true,"result":{"feedback_enabled":false,"max_context_bytes":1048576,"max_request_bytes":1048576,"operations":["hello","scan","query","context","feedback"],"protocol":"slither.agent/v1","schemas":["slither.report/v1","slither.context/v1","slither.outcome/v1","slither.eval/v1"]}}
```

Operations: `hello` (protocol state), `scan` (rebuild and report snapshot facts), `query` (bounded row summaries, limit 1–80), `context` (redacted, byte-bounded `slither.context/v1` packet), and `feedback` (requires `--outcomes`). Error responses carry a stable code such as `invalid_request` and never leak paths or raw errors.

### Outcome ledger and evaluation

Pass `--outcomes <path>` to `agent` to opt into feedback persistence; without it, `feedback` returns `feedback_disabled`. The parent directory must exist; slither creates the ledger owner-only and appends one typed `slither.outcome/v1` JSON record per verdict — no paths, source, or prose (see [Non-goals](#non-goals)).

Evaluate report JSON files against a ledger (exactly one `--outcomes`, one or more `--report`, exactly one of `--json`/`--markdown`; `--out` defaults to `-`):

```bash
slither eval --outcomes ./slither-outcomes.jsonl --report slither-report.json --json --out -
```

Expected result: one deterministic `slither.eval/v1` JSON object with matched outcome totals, calibration metrics (fixed `top_k` of 15, noise rate, recall, coverage, score saturation), and warning counts. Exact formulas are specified in [docs/usage.md](docs/usage.md).

### Readiness and shell surfaces

- `slither doctor [--json]` — read-only readiness: config state, model mode, key presence, and a loopback-only preflight of the configured local model. Warnings exit successfully.
- `slither outputs [surface] [--json]` — catalog of every output surface with its schema, privacy, and flags.
- `slither completion bash|zsh` — dependency-free completion script (source-checked, not executed): `source <(slither completion bash)`.
- `slither version [--build|--json]` — build provenance; `--json` emits `slither.version/v1`.

## Verification

The repo's own gate (from `justfile`) is fmt + full tests + vet:

```bash
gofmt -w cmd/slither internal/slither
go test ./...
go vet ./...
```

Build a binary with `go build -o slither ./cmd/slither`.

Two workspace facts shape building this checkout. First, `go.mod` replaces `github.com/garyblankenship/wormhole/v3` with the sibling path `../wormhole` and `github.com/dotcommander/verdict` with `../../dotcommander/verdict`, so a bare Git clone fails to build (`replacement directory ../wormhole does not exist`); both sibling checkouts must be present at those relative paths. Second, if a parent Go workspace (`go.work`) does not include this module, prefix Go commands with `GOWORK=off`. Use `go install github.com/dotcommander/slither/cmd/slither@latest` to install the published release instead; it resolves its dependencies from the module proxy.

License: MIT ([LICENSE](LICENSE)). Release notes: [CHANGELOG.md](CHANGELOG.md).

## Limits

- Scanning skips these directories: `.git`, `node_modules`, `vendor`, `dist`, `build`, `target`, `coverage`, `.next`, `.svelte-kit`, `.venv`, `.work`; and these suffixes: `.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.pdf`, `.zip`, `.gz`, `.tar`, `.mp4`, `.mp3`, `.lock`, `.sum`.
- A file whose first 4096 bytes contain a NUL byte is treated as binary and skipped. Per-file summaries are truncated to 180 characters.
- Non-test source files of 80+ lines get a `test-gap` reason; files of 300+ lines get a `size:<n> lines` reason.
- Reports and summaries contain repository paths and bounded redacted evidence; treat them as private when sharing. The outcome ledger, eval JSON, and eval Markdown contain none of those.
- Agent requests and context packets are capped at 1 MiB each; eval caps each ledger record at 1 MiB. Full protocol limits: [docs/usage.md](docs/usage.md).
