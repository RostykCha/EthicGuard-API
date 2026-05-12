.PHONY: build test test-race lint cover cover-check run tidy verify

build:
	go build ./...

test:
	go test ./...

# Same flags CLAUDE.md "Definition of done" demands: race detector + no cache.
test-race:
	go test -race -count=1 ./...

lint:
	golangci-lint run

# `cover` runs the test suite with a coverage profile and reports per-package
# numbers. Read-only — never fails on coverage shortfalls. Use for local
# inspection before pushing.
cover:
	go test -coverprofile=coverage.out ./...
	go run ./scripts/cover-check -profile coverage.out -floor 70

# `cover-check` is the CI gate. Same flags as `cover` plus -enforce so any
# covered package below the floor exits non-zero. Wired into `verify`.
# Excluded paths (no-op packages, SQL-only dirs) are listed in
# scripts/cover-check/main.go.
cover-check:
	go test -coverprofile=coverage.out ./...
	go run ./scripts/cover-check -profile coverage.out -floor 70 -enforce

# One-shot "is this PR ready" check. Mirrors the three steps in CLAUDE.md
# "Definition of done" + a coverage report. The floor is reported but NOT
# enforced until store/llm/httpapi coverage is backfilled — see the
# iteration plan. Swap `cover` -> `cover-check` here to enforce.
verify: build test-race lint cover

run:
	go run ./cmd/ethicguard-api

tidy:
	go mod tidy
