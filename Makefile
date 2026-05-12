.PHONY: build test test-race lint run tidy verify

build:
	go build ./...

test:
	go test ./...

# Same flags CLAUDE.md "Definition of done" demands: race detector + no cache.
test-race:
	go test -race -count=1 ./...

lint:
	golangci-lint run

# One-shot "is this PR ready" check. Mirrors the three steps in CLAUDE.md
# "Definition of done" so an agent can run a single command instead of
# remembering them.
verify: build test-race lint

run:
	go run ./cmd/ethicguard-api

tidy:
	go mod tidy
