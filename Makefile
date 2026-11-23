# Every developer command. `make help` lists them.
SHELL := /bin/bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

ROOT := $(CURDIR)
include versions.env
export RULES_DIR := $(ROOT)/rules
export LEGAL_DIR := $(ROOT)/legal

help: ## list the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

run: ## run the server from source on MCP_PORT (default 8090)
	@echo "+ go run ./cmd/mcp"
	go run ./cmd/mcp

test: ## unit, tool, transport and rule-pack tests (corpus references included)
	@echo "+ go test ./..."
	go test -count=1 ./...

lint: ## go vet and golangci-lint
	@echo "+ go vet ./... && golangci-lint run"
	go vet ./...
	golangci-lint run ./...

fmt: ## gofmt and goimports through golangci-lint
	golangci-lint fmt ./...

lint-workflows: ## actionlint over .github/workflows
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

vuln: ## govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

legal-check: ## legal corpus: manifest, originals, sha256, frontmatter, dates, INDEX.md
	@echo "+ python3 -I scripts/legal_fetch.py --check"
	python3 -I scripts/legal_fetch.py --check

legal-fetch: ## download the originals in legal/manifest.yaml and rebuild legal/corpus (network, pdftotext)
	python3 -I scripts/legal_fetch.py

image: ## build the container image
	docker build --build-arg GO_IMAGE=$(GO_IMAGE) -t vietnam-tax-mcp:dev .

check: lint test legal-check ## everything CI runs

.PHONY: help run test lint lint-workflows fmt vuln legal-check legal-fetch image check
