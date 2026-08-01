# Thin task index. Every target delegates to the native tool.
.DEFAULT_GOAL := help
.PHONY: help build test lint run interop docs docs-serve clean

help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "%-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the diagd binary
	go build ./cmd/diagd

test: ## Run unit tests with the race detector
	go test -race ./...

lint: ## Check formatting and vet, the same checks CI runs
	test -z "$$(gofmt -l .)"
	go vet ./...

run: ## Run the server locally with debug logging
	go run ./cmd/diagd serve -log-level debug

interop: ## Run the OB-UDPST reference client interoperability suite
	./scripts/interop.sh

docs: ## Build the documentation site strictly (needs mkdocs, see requirements.txt)
	mkdocs build --strict

docs-serve: ## Serve the documentation locally on 127.0.0.1:8000
	mkdocs serve

clean: ## Remove build artifacts
	rm -rf diagd site
