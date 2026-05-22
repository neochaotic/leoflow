# Leoflow Makefile
# All targets assume execution from the repository root.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

# ─── Tool versions (pinned; see ADR 0012 / 0014) ───
GOLANGCI_LINT_VERSION ?= v2.12.2
GOREPORTCARD_VERSION  ?= latest
GOVULNCHECK_VERSION   ?= latest
MIGRATE_VERSION       ?= latest
SQLC_VERSION          ?= latest

# ─── Paths ───
BIN_DIR       := bin
CLI_BINARY    := $(BIN_DIR)/leoflow
SERVER_BINARY := $(BIN_DIR)/leoflow-server
AGENT_BINARY  := $(BIN_DIR)/leoflow-agent

# ─── Build metadata (embedded via internal/version) ───
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/neochaotic/leoflow/internal/version
LDFLAGS := -s -w \
	-X '$(VERSION_PKG).version=$(VERSION)' \
	-X '$(VERSION_PKG).gitCommit=$(GIT_COMMIT)' \
	-X '$(VERSION_PKG).buildDate=$(BUILD_DATE)'

# ─── Database (used by migrate targets; override via env) ───
DATABASE_URL ?= postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Install Go tools, Python parser, and the pre-commit hook
	go mod download
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/gojp/goreportcard/cmd/goreportcard-cli@$(GOREPORTCARD_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	command -v python3 >/dev/null && pip install -e "./parser[dev]" || echo "skip parser install (python3 not found)"
	install -m 0755 scripts/pre-commit .git/hooks/pre-commit
	@echo "setup complete"

.PHONY: build
build: ## Build all binaries into ./bin
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(CLI_BINARY) ./cmd/leoflow
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(SERVER_BINARY) ./cmd/leoflow-server
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(AGENT_BINARY) ./cmd/leoflow-agent

.PHONY: runtime-images
runtime-images: ## Build the task base images for each supported Python version
	for v in 3.10 3.11 3.12; do \
		docker build -f runtime/Dockerfile --build-arg PYTHON_VERSION=$$v -t leoflow-base:py$$v . ; \
	done

.PHONY: test
test: ## Run Go and Python tests with coverage
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	command -v pytest >/dev/null && (cd parser && pytest -v --cov=leoflow_parser) || echo "skip pytest (not installed)"
	command -v pytest >/dev/null && (cd runtime/python && pytest -v --cov=leoflow_runtime) || echo "skip runtime pytest (not installed)"

.PHONY: cover
cover: test ## Show total Go coverage
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: ## Run golangci-lint and ruff
	golangci-lint run ./...
	command -v ruff >/dev/null && (cd parser && ruff check .) || echo "skip ruff (not installed)"
	command -v ruff >/dev/null && (cd runtime/python && ruff check .) || echo "skip runtime ruff (not installed)"

.PHONY: fmt
fmt: ## Format Go code
	gofmt -w .

.PHONY: reportcard
reportcard: ## Verify Go Report Card grade is A+ (ADR 0012)
	goreportcard-cli -v

.PHONY: vuln
vuln: ## Run govulncheck (ADR 0014)
	govulncheck ./...

.PHONY: dev-up
dev-up: ## Start local Postgres + Redis (wait healthy) and apply migrations
	docker compose up -d --wait
	$(MAKE) migrate-up

.PHONY: dev-down
dev-down: ## Stop local Postgres + Redis, keeping data
	docker compose down

.PHONY: dev-reset
dev-reset: ## Wipe local Postgres + Redis data and restart fresh
	docker compose down -v
	$(MAKE) dev-up

.PHONY: migrate-up
migrate-up: ## Apply all up migrations
	migrate -path migrations -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	migrate -path migrations -database "$(DATABASE_URL)" down 1

.PHONY: sqlc
sqlc: ## Regenerate sqlc code
	sqlc generate

.PHONY: proto
proto: ## Regenerate protobuf/gRPC code from proto/ via buf
	buf generate

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
