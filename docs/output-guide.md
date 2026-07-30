# Choosing a Slither output

Start with `slither outputs` for a catalog of producers, schemas, limits,
privacy, flags, stable errors, and typed agent operands. Add `--json` for the stable
`slither.output-catalog/v1` form.

| Need | Command | Output |
| --- | --- | --- |
| Read full evidence | `slither report REPO` | Full Markdown report |
| Automate all evidence | `slither report REPO --json` | `slither.report/v1` |
| Start a review quickly | `slither report REPO --summary` | Concise Markdown summary |
| Automate compact triage | `slither report REPO --summary --json` | `slither.summary/v1` |
| Triage review budget | `slither report REPO --cull --json` | Report plus cull ledger |
| Drive a bounded agent | `slither agent REPO` | `slither.agent/v1` JSONL |
| Get bounded context | agent `context` | `slither.context/v1` |
| Persist verdicts | `agent --outcomes` feedback | `slither.outcome/v1` JSONL |
| Evaluate labels | `slither eval ... --json` | `slither.eval/v1` |
| Inspect evaluation safely | `slither eval ... --markdown` | Private Markdown |
| Check local readiness | `slither doctor --json` | `slither.doctor/v1` |
| Record build provenance | `slither version --json` | `slither.version/v1` |

## Compatibility and privacy

`slither.report/v1`, `slither.agent/v1`, `slither.context/v1`,
`slither.outcome/v1`, and `slither.eval/v1` remain unchanged. Summary, catalog,
version JSON, and doctor JSON are opt-in schemas.

Full reports and summaries contain repository paths. Agent context is redacted
and bounded. The outcome ledger, eval JSON, and eval Markdown intentionally do
not include repository paths, input filenames, report IDs, timestamps, source,
snippets, prompts, or credentials.

## Readiness and shell use

`slither doctor` never creates configuration, scans a repository, writes cache
state, sends prompts, or contacts a non-loopback endpoint. It rejects endpoint
userinfo, strips authorization, disables proxies and redirects, resolves before
dialing, and accepts only loopback IP results. A configured loopback model gets
an unauthenticated `/models` preflight with a two-second timeout and one MiB
response cap. Warnings exit successfully; malformed config is an error.

```bash
source <(slither completion bash)
# or: source <(slither completion zsh)
```

Every command supports `--help` and `-h`; `slither help COMMAND` prints the
same command-specific reference. Usage errors end with the next help command.
