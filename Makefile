# Local mirror of .github/workflows/ci.yml.
#
# `make ci` runs the gating checks that don't need Docker: lint, module
# tidiness, vulnerability scan, build, unit/Postgres tests, boot smoke, and
# a fuzz smoke. Postgres-backed tests run when KEYCHAIN_TEST_POSTGRES_URL is
# set — `make postgres-up` starts a throwaway Postgres on :5432.
#
# `make ci-full` adds the docker-compose end-to-end test.
#
# Tool versions are pinned to match CI; bump them in lockstep with the
# workflow env block.

SHELL := /bin/bash

GOLANGCI_LINT_VERSION ?= v2.12.1
GOVULNCHECK_VERSION   ?= v1.1.4

GO            ?= go
BUF           ?= buf
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK   ?= govulncheck

LINT_BASE_REV ?= origin/main

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Aggregate targets
# ---------------------------------------------------------------------------

.PHONY: ci
ci: lint tidy-check vuln build test smoke fuzz ## Run all CI gates that don't need Docker
	@echo "==> make ci: all gates passed"

.PHONY: ci-full
ci-full: ci e2e ## ci + docker-compose end-to-end
	@echo "==> make ci-full: passed (incl. docker e2e)"

# ---------------------------------------------------------------------------
# Individual gates
# ---------------------------------------------------------------------------

.PHONY: lint
lint: ## golangci-lint, only new issues vs $(LINT_BASE_REV)
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not installed. Run 'make install-tools' or:"; \
		echo "  brew install golangci-lint   # or"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)"; \
		exit 1; \
	}
	$(GOLANGCI_LINT) run --config=.golangci.yml --new-from-rev=$(LINT_BASE_REV) ./...

.PHONY: lint-all
lint-all: ## golangci-lint over the entire tree
	$(GOLANGCI_LINT) run --config=.golangci.yml ./...

.PHONY: fmt
fmt: ## Apply gofumpt formatting via golangci-lint
	$(GOLANGCI_LINT) fmt --config=.golangci.yml ./...

.PHONY: proto
proto: ## buf lint + regenerate protobuf bindings
	$(BUF) lint
	$(BUF) generate

.PHONY: tidy
tidy: ## Run go mod tidy
	$(GO) mod tidy

.PHONY: tidy-check
tidy-check: ## Verify go.mod / go.sum are tidy
	$(GO) mod tidy
	@if ! git diff --exit-code -- go.mod go.sum; then \
		echo "go.mod / go.sum out of sync — run 'make tidy' and commit the result." >&2; \
		exit 1; \
	fi

.PHONY: vuln
vuln: ## govulncheck
	@command -v $(GOVULNCHECK) >/dev/null 2>&1 || { \
		echo "govulncheck not installed. Run 'make install-tools' or:"; \
		echo "  go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)"; \
		exit 1; \
	}
	$(GOVULNCHECK) ./...

.PHONY: build
build: ## go build ./...
	$(GO) build ./...

.PHONY: test
test: ## Unit + Postgres tests with race detector
	$(GO) test -count=1 -race -timeout=600s ./...

.PHONY: test-cover
test-cover: ## Coverage profile + per-package gates
	bash scripts/run-coverage.sh
	bash scripts/coverage-gate.sh cover.out --config .coverage-gates.yml

.PHONY: smoke
smoke: ## Boot smoke tests (tests/smoke)
	@if compgen -G "tests/smoke/*.go" > /dev/null; then \
		$(GO) test -tags=smoke -timeout=120s ./tests/smoke/...; \
	else \
		echo "no smoke tests under tests/smoke — skipping"; \
	fi

.PHONY: fuzz
fuzz: ## Fuzz smoke — seed corpus + 15s per target
	@set -euo pipefail; \
	targets=$$( \
		grep -rEn --include='*_test.go' '^func (Fuzz[A-Za-z0-9_]+)\(' . \
			| sed -E 's|^(.*)/[^/]+:[0-9]+:func (Fuzz[A-Za-z0-9_]+).*$$|\1 \2|' \
			| sort -u \
	); \
	if [ -z "$$targets" ]; then \
		echo "no fuzz targets — skipping"; \
		exit 0; \
	fi; \
	echo "$$targets" | while read -r dir name; do \
		echo "==> fuzz $$name in $$dir"; \
		$(GO) test -run="^$${name}$$" -timeout=120s "./$$dir"; \
		$(GO) test -run='^$$' -fuzz="^$${name}$$" -fuzztime=15s -parallel=4 -timeout=120s "./$$dir"; \
	done

.PHONY: e2e
e2e: ## Docker-compose end-to-end
	test/e2e/run-docker-compose-suite.sh

# ---------------------------------------------------------------------------
# Local services
# ---------------------------------------------------------------------------

.PHONY: postgres-up
postgres-up: ## Start a throwaway Postgres on :5432 for local tests
	docker run -d --rm -p 5432:5432 \
		-e POSTGRES_USER=keychain -e POSTGRES_PASSWORD=keychain -e POSTGRES_DB=keychain \
		--name keychain-postgres postgres:16.13-alpine3.23
	@echo "export KEYCHAIN_TEST_POSTGRES_URL=postgres://keychain:keychain@localhost:5432/keychain?sslmode=disable"

.PHONY: postgres-down
postgres-down: ## Stop the throwaway Postgres
	docker rm -f keychain-postgres >/dev/null 2>&1 || true

.PHONY: docker
docker: ## Build the service image
	docker build --target server -t keychain:local .

.PHONY: dev
dev: ## Start local Postgres + keychain via docker-compose
	docker compose up -d --build

.PHONY: stop
stop: ## Stop local docker-compose services
	docker compose down

.PHONY: clean
clean: ## Stop local services and remove volumes
	docker compose down -v --remove-orphans

# ---------------------------------------------------------------------------
# Tooling install
# ---------------------------------------------------------------------------

.PHONY: install-tools
install-tools: ## Install pinned lint + vuln tooling
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "}; /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
