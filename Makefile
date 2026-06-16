# Job Agent — developer tasks.
# Override DATABASE_URL / CONFIG as needed:
#   make migrate-up DATABASE_URL=postgres://...

CONFIG       ?= config.yaml
MIGRATIONS   ?= ./migrations
DATABASE_URL ?= postgres://jobagent:jobagent@localhost:5432/jobagent?sslmode=disable
BIN          ?= bin/agent

.PHONY: build test lint vet run-once run migrate-up migrate-down \
        docker-build docker-run tidy clean

build: ## Build the agent and test-scraper binaries
	go build -o $(BIN) ./cmd/agent
	go build -o bin/test-scraper ./cmd/test-scraper

test: ## Run unit tests
	go test ./...

test-integration: ## Run integration tests (needs Docker for testcontainers)
	go test -tags=integration -count=1 ./...

vet: ## go vet
	go vet ./...

lint: ## Run golangci-lint if installed, else go vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; running go vet"; go vet ./...; \
	fi

run-once: build ## Run a single cycle (dry-run honours config)
	$(BIN) -config $(CONFIG) -migrations $(MIGRATIONS) -once

run: build ## Run the agent as a daemon
	$(BIN) -config $(CONFIG) -migrations $(MIGRATIONS)

migrate-up: build ## Apply all migrations
	$(BIN) -config $(CONFIG) -migrations $(MIGRATIONS) -migrate-only

migrate-down: build ## Roll back all migrations
	$(BIN) -config $(CONFIG) -migrations $(MIGRATIONS) -migrate-down

docker-build: ## Build the production image
	docker build -t job-agent:latest .

docker-run: ## Bring up postgres + agent via compose
	docker compose up --build

tidy: ## Tidy go.mod / go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -rf bin screenshots

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
