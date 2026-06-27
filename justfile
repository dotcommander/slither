set positional-arguments

default:
    @just --list

fmt:
    gofmt -w cmd/slither internal/slither

test:
    go test ./internal/slither

test-all:
    go test ./...

race:
    go test -race ./internal/slither

vet:
    go vet ./...

build:
    go build -o slither ./cmd/slither

install:
    go install ./cmd/slither

check: fmt test-all vet

report repo="." out="slither-report.md" top="80" days="90":
    go run ./cmd/slither report {{repo}} --out {{out}} --top {{top}} --days {{days}}

report-json repo="." out="slither-report.json":
    go run ./cmd/slither report {{repo}} --json --out {{out}}

cull repo="." out="slither-cull.json" top="80":
    go run ./cmd/slither report {{repo}} --top {{top}} --cull --json --out {{out}}

local-report repo="." out="slither-report.md":
    go run ./cmd/slither report {{repo}} --local --out {{out}}
