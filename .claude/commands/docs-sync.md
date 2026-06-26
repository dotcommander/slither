---
description: Create basic docs with example usage if missing, then keep docs/, README.md, and AGENTS.md current with recently changed source. Optional arg = lookback window (default 15m).
argument-hint: "[window e.g. 15m | 1h]"
allowed-tools: Bash(git*), Bash(find*), Read, Edit, Write, Grep, Glob, Agent
---

# docs-sync — keep docs current with code

Run the documentation-sync workflow for this repository. `$ARGUMENTS` is an
optional lookback window (e.g. `15m`, `1h`); default to **15m** when empty.

## Phase 1 — Bootstrap (only if docs are missing)

If `docs/` has no usage/overview doc (e.g. `docs/usage.md` absent):

1. Read the CLI entry point and command parsing to learn commands, flags,
   defaults, and behavior (do not guess flag names or defaults — read source).
2. Read `README.md` and `AGENTS.md` for existing examples and tone.
3. Write `docs/usage.md` with: a one-paragraph overview, build/run commands, a
   flag table (name, default, description — all from source), worked example
   commands, output description, and security notes.

Then stop — bootstrap is the whole run. On the next invocation, Phase 2 runs.

## Phase 2 — Incremental sync (docs already exist)

1. **Find changed files in the window.** Prefer git; fall back to mtime:
   - git repo: `git -C <repo> diff --name-only @{<window> ago} 2>/dev/null` or
     `git log --since=<window> --name-only --pretty=format: | sort -u`; also
     include uncommitted changes via `git status --porcelain`.
   - no git / empty result: `find <repo> -name '*.go' -mmin -<window-in-min> -not -path '*/.git/*'`.
2. **If nothing changed in the window, stop** and report "no changes — docs current".
3. **Decide if docs are affected.** Docs need updating only when the changes
   touch user-visible surface: CLI commands/flags/defaults, output format,
   public behavior, build/run/install steps, or project layout. Pure internal
   refactors, tests, and comments do NOT require doc changes — say so and stop.
4. **Bring affected docs current** — update only what changed, matching each
   file's existing structure and tone:
   - `docs/usage.md` — flags, examples, output, commands.
   - `README.md` — top-level examples and the elevator pitch.
   - `AGENTS.md` — project structure, build/test/dev commands, conventions.
   Verify every flag name, default, and command against the actual source
   before writing — never document an interface you have not confirmed.

## Rules

- Read source to confirm interfaces; documentation must match real behavior.
- Make the smallest correct edit. Do not rewrite docs that are already accurate.
- Do not invent flags, defaults, commands, or examples.
- Before declaring a doc statement wrong, grep the whole package for the
  feature — absence in the CLI entry file is not absence in the code. Defaults
  can live anywhere (e.g. a `//go:embed` catalog used when a flag is empty), so
  confirm against all source files, not just the command-parsing file.
- All code/doc writes go through the project's executor agent, not direct Edit
  in the orchestrator, per the repo's editing rules.
- After writing, if the tree builds/docs are self-consistent, commit with a
  `docs(<scope>): <summary>` subject.

## Report

End with a 2–4 line summary: window used, files changed in window, which docs
were updated (or "none — docs current / changes internal only"), and the commit
subject if one was made.
