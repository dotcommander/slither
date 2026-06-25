# slither

`slither` is a cheap-model repo scout. It creeps like a snake through a repository, gathers bounded file evidence, optionally asks a cheap model through `github.com/garyblankenship/wormhole`, and writes a Markdown report.

```bash
go run ./cmd/slither report /path/to/repo --out slither-report.md --top 80 --days 90
```

Emit a machine-readable evidence envelope:

```bash
go run ./cmd/slither report /path/to/repo --json --out slither-report.json
```

Append an auditable cheap-model cull ledger with kept targets, alternates, culled buckets, evidence intersections, and skipped-signal context:

```bash
go run ./cmd/slither report /path/to/repo --top 80 --cull --json --out slither-cull.json
```

`slither` embeds the full `premium-model-triage` pattern catalog by default. Use `--patterns` only when testing or overriding that catalog:

```bash
go run ./cmd/slither report /path/to/repo \
  --patterns /Users/vampire/.codex/skills/premium-model-triage/references/triage_patterns.json \
  --json --out slither-report.json
```

With OpenRouter via wormhole:

```bash
OPENROUTER_API_KEY=... go run ./cmd/slither report /path/to/repo \
  --model z-ai/glm-5.2 \
  --base-url https://openrouter.ai/api/v1 \
  --out slither-report.md
```

With a local OpenAI-compatible server:

```bash
go run ./cmd/slither report /path/to/repo --local --out slither-report.md
```

If no model is configured, `slither` uses a deterministic fallback score so the CLI is useful offline. Reports include discovery counts, evidence layers, lane scores, pattern source, and skipped signals so missing evidence is visible instead of treated as low risk.
