# slither

Run an offline repository scout immediately:

```bash
go run ./cmd/slither report /path/to/repo --out slither-report.md --top 80 --days 90
```

`slither` gathers bounded per-file evidence and writes a Markdown or JSON review
queue. With no model configured it uses deterministic fallback scoring, so this
command has no network or API-key requirement.

## Install

```bash
go install github.com/dotcommander/slither/cmd/slither@latest
```

Or build from source:

```bash
git clone https://github.com/dotcommander/slither.git
cd slither
go build -o slither ./cmd/slither
```

## Common commands

Choose an output or check safe local readiness:

```bash
go run ./cmd/slither outputs
go run ./cmd/slither doctor --json
go run ./cmd/slither report /path/to/repo --summary
```

Emit the machine-readable report envelope:

```bash
go run ./cmd/slither report /path/to/repo --json --out slither-report.json
```

Include the auditable cull ledger:

```bash
go run ./cmd/slither report /path/to/repo --top 80 --cull --json --out slither-cull.json
```

Run with OpenRouter through `github.com/garyblankenship/wormhole`:

```bash
OPENROUTER_API_KEY=... go run ./cmd/slither report /path/to/repo \
  --model z-ai/glm-5.2 \
  --base-url https://openrouter.ai/api/v1 \
  --out slither-report.md
```

Use a local OpenAI-compatible server:

```bash
go run ./cmd/slither report /path/to/repo --local --out slither-report.md
```

The embedded `premium-model-triage` catalog is the default. Use `--patterns`
only to test or deliberately override it.

## Agent and evaluation

The dependency-free agent bridge accepts sequential JSONL on standard input:

```bash
printf '%s\n' '{"schema":"slither.agent/v1","id":"hello-1","op":"hello"}' |
  go run ./cmd/slither agent /path/to/repo
```

To opt into the private outcome ledger, pass its path explicitly; feedback is
otherwise disabled:

```bash
go run ./cmd/slither agent /path/to/repo --outcomes ./slither-outcomes.jsonl
```

Evaluate one or more JSON reports against that ledger:

```bash
go run ./cmd/slither eval \
  --outcomes ./slither-outcomes.jsonl \
  --report slither-report.json \
  --json --out -
```

See [docs/usage.md](docs/usage.md) for flag details, the agent JSONL contract,
outcome-ledger privacy rules, and exact evaluation formulas;
[docs/output-guide.md](docs/output-guide.md) for output selection; and
[CHANGELOG.md](CHANGELOG.md) for current release notes.
