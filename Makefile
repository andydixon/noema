SHELL := /bin/bash
GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/andydixon/noema/internal/version.Version=$(VERSION)
DEV_DB ?= postgres://noema:noema@127.0.0.1:5433/noema?sslmode=disable

.PHONY: build test test-race test-db lint vet fmt run migrate migrate-down docker dev dev-db dev-down security clean tools help

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n",$$1,$$2}'

build: ## build noemad and noemactl into ./bin
	go build -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

test: ## unit tests
	go test ./...

test-race: ## unit tests with the race detector
	go test -race ./...

test-db: dev-db ## tests that need PostgreSQL (NOEMA_TEST_DATABASE_URL)
	NOEMA_TEST_DATABASE_URL=$(DEV_DB) go test -race -count=1 ./...

vet: ## go vet
	go vet ./...

lint: vet ## staticcheck
	staticcheck ./...

fmt: ## gofmt
	gofmt -l -w $$(git ls-files '*.go')

security: ## gosec + govulncheck
	gosec -quiet -exclude=G104 ./... ; govulncheck ./...

run: build ## run the daemon with .env
	set -a; [ -f .env ] && source .env; set +a; ./bin/noemad

migrate: build ## apply migrations to $(DEV_DB)
	NOEMA_DATABASE_URL=$(DEV_DB) ./bin/noemactl migrate up

migrate-down: build ## roll back one migration
	NOEMA_DATABASE_URL=$(DEV_DB) ./bin/noemactl migrate down

dev-db: ## start PostgreSQL (pgvector) via docker compose
	docker compose -f deploy/docker-compose.yml up -d db
	@until docker compose -f deploy/docker-compose.yml exec -T db pg_isready -U noema >/dev/null 2>&1; do sleep 0.5; done

dev: dev-db migrate ## start database, migrate, run daemon
	$(MAKE) run

dev-down: ## stop development containers
	docker compose -f deploy/docker-compose.yml down

docker: ## build container image
	docker build -f deploy/Dockerfile -t noema:$(VERSION) .

tools: ## install developer tools
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest

clean: ## remove build output
	rm -rf bin dist coverage.*
