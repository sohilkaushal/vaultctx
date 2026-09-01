.PHONY: build check fmt fmt-check race test vet

VERSION ?= dev

build:
	mkdir -p bin
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/vaultctx ./cmd/vaultctx

fmt:
	gofmt -w ./cmd ./internal

fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)"

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

check: fmt-check vet test
