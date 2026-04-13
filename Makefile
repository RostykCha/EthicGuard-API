.PHONY: build test lint run tidy

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run

run:
	go run ./cmd/ethicguard-api

tidy:
	go mod tidy
