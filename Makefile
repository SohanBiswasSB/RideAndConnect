SHELL := /usr/bin/env bash

# Conda env activation. The user activates `conda activate RideAndConnect` in their shell;
# inside this Makefile we re-source it so subshells find Go 1.26.2.
CONDA_BASE := /opt/homebrew/Caskroom/miniforge/base
CONDA_ENV  := RideAndConnect
ACTIVATE   := source $(CONDA_BASE)/etc/profile.d/conda.sh && conda activate $(CONDA_ENV) && export CGO_ENABLED=0 &&

DATABASE_URL ?= postgres://rideandconnect:rideandconnect@127.0.0.1:5432/rideandconnect?sslmode=disable
MIGRATE_DIR  := migrations

.DEFAULT_GOAL := help

.PHONY: help
help:
	@awk 'BEGIN{FS=":.*##"; printf "\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: tidy
tidy: ## go mod tidy
	$(ACTIVATE) go mod tidy

.PHONY: fmt
fmt: ## gofumpt + goimports
	$(ACTIVATE) gofumpt -w . && goimports -w .

.PHONY: lint
lint: ## golangci-lint run
	$(ACTIVATE) golangci-lint run --timeout=5m ./...

.PHONY: vet
vet: ## go vet ./...
	$(ACTIVATE) go vet ./...

.PHONY: test
test: ## go test -race ./...
	$(ACTIVATE) go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## integration tests (require docker compose up postgres)
	$(ACTIVATE) go test -race -count=1 -tags=integration ./...

.PHONY: vuln
vuln: ## govulncheck
	$(ACTIVATE) govulncheck ./...

.PHONY: build
build: ## build cmd/api binary
	$(ACTIVATE) go build -trimpath -ldflags="-s -w -X main.version=$$(git describe --tags --always 2>/dev/null || echo dev) -X main.commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none) -X main.buildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/api ./cmd/api

.PHONY: run
run: ## run cmd/api locally
	$(ACTIVATE) go run ./cmd/api

.PHONY: db-up
db-up: ## docker compose up postgres
	docker compose up -d postgres

.PHONY: db-down
db-down: ## docker compose down
	docker compose down

.PHONY: db-logs
db-logs:
	docker compose logs -f postgres

.PHONY: migrate-up
migrate-up: ## apply all migrations
	$(ACTIVATE) migrate -path $(MIGRATE_DIR) -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## roll back one migration
	$(ACTIVATE) migrate -path $(MIGRATE_DIR) -database "$(DATABASE_URL)" down 1

.PHONY: migrate-force
migrate-force: ## force migration version (use VERSION=N)
	$(ACTIVATE) migrate -path $(MIGRATE_DIR) -database "$(DATABASE_URL)" force $(VERSION)

.PHONY: migrate-new
migrate-new: ## create new migration files (use NAME=foo)
	$(ACTIVATE) migrate create -ext sql -dir $(MIGRATE_DIR) -seq $(NAME)

.PHONY: sqlc
sqlc: ## generate type-safe queries
	$(ACTIVATE) sqlc generate

.PHONY: schema-sync
schema-sync: ## copy schema/schema.graphqls into internal/graph (the embed source)
	cp schema/schema.graphqls internal/graph/schema.graphqls

.PHONY: seed
seed: ## seed the dev DB with the canonical 3 users (default password = phone)
	$(ACTIVATE) DATABASE_URL="$(DATABASE_URL)" go run ./cmd/seed

.PHONY: tools
tools: ## install dev tools into the conda env
	$(ACTIVATE) go install mvdan.cc/gofumpt@latest
	$(ACTIVATE) go install golang.org/x/tools/cmd/goimports@latest
	$(ACTIVATE) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(ACTIVATE) go install golang.org/x/vuln/cmd/govulncheck@latest
	$(ACTIVATE) go install -tags 'postgres,file' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	$(ACTIVATE) go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

.PHONY: docker-build
docker-build: ## build production container image
	docker build -t rideandconnect/api:dev .

.PHONY: clean
clean:
	rm -rf bin/
