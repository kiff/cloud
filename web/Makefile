# Makefile for the cloud web binary.
#
# Most targets are thin wrappers over `go` and the templ CLI. CSS is
# hand-written and committed (no build step beyond `go build`).
# See README.md for install instructions.

.PHONY: run build test vet templ clean

# Default port matches cmd/web/main.go.
ADDR ?= :8081

run:
	go run ./cmd/web -addr=$(ADDR)

build:
	go build -o bin/web ./cmd/web

test:
	go test ./...

vet:
	go vet ./...

# Regenerate templ-compiled .go files from .templ sources.
# Requires: go install github.com/a-h/templ/cmd/templ@latest
templ:
	templ generate

clean:
	rm -rf bin
