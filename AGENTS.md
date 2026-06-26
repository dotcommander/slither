# Repository Guidelines

## Project Structure & Module Organization

This repository is a small Go CLI module named `private/slither`. The command entry point is `cmd/slither/main.go`; keep process-level concerns such as argument wiring and exit behavior there. Core behavior lives in `internal/slither/`, including CLI parsing, scanning, AI scoring, report generation, and shared types. Tests currently live beside the package code as `internal/slither/*_test.go`. The root `README.md` documents user-facing examples, and `go.mod` pins the module plus a local `replace` for `github.com/garyblankenship/wormhole`.

## Build, Test, and Development Commands

- `go test ./internal/slither`: run the focused package tests.
- `go test ./...`: run all Go tests in the module.
- `go run ./cmd/slither report /path/to/repo --out slither-report.md --top 80`: run the CLI against a repository using deterministic fallback scoring when no model is configured.
- `go run ./cmd/slither report /path/to/repo --local --out slither-report.md`: use a local OpenAI-compatible model server.
- `gofmt -w cmd/slither internal/slither`: format touched Go files.

## Coding Style & Naming Conventions

Use standard Go formatting and idioms. Keep exported behavior in `internal/slither` explicit and testable, with `context.Context` passed through I/O, model calls, and scans. Prefer small plain functions, early returns, and concrete structs over speculative interfaces. Use descriptive Go names such as `BuildReport`, `RenderMarkdown`, and `FileEvidence`; tests should name the behavior under test.

## Loop & Recursion Review

Treat loops and recursion as structural hotspots. When touching a loop, recursive function, walker, scanner, graph traversal, retry, or batch processor, check whether work is bounded, independent, cancellable, and deterministic after collection. Prefer bounded worker pools for independent file or item scans, preserve output order with a final sort when order matters, and run `go test -race` when concurrency or shared state changes. For recursion, check depth bounds, cycle detection, repeated work, and whether an iterative form is safer.

## Testing Guidelines

Use Go's standard `testing` package. Add tests next to the code they exercise with names like `TestBuildReportFallbackScoresRiskyFiles`. Prefer temporary directories via `t.TempDir()` for filesystem behavior. When changing CLI argument handling, scoring, scan limits, or Markdown output, add or update focused tests and run at least `go test ./internal/slither`.

## Commit & Pull Request Guidelines

This checkout does not include Git history, so no repository-specific commit convention can be inferred. Use concise imperative commit subjects, for example `Add report scoring test`. Pull requests should describe the user-visible behavior change, list the commands run, and call out any model/API configuration required to reproduce behavior. Include report snippets only when they clarify a CLI-output change.

## Security & Configuration Tips

Do not commit API keys or generated reports containing private repository data. Pass OpenRouter credentials through `OPENROUTER_API_KEY`, and keep local model endpoints configurable with flags such as `--base-url` and `--local`.
