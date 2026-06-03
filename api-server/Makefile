SHELL := /bin/sh

.PHONY: help env deps db-up db-down dev docker-up docker-down test coverage coverage-html swagger-check smoke fmt

help:
	@echo "Taravelis API local commands"
	@echo ""
	@echo "  make env            Create .env from .env.example if missing"
	@echo "  make deps           Download Go modules"
	@echo "  make db-up          Start Postgres/PostGIS, Redis, and MinIO"
	@echo "  make db-down        Stop local infrastructure"
	@echo "  make dev            Run API locally with go run"
	@echo "  make docker-up      Run full stack in Docker"
	@echo "  make docker-down    Stop full Docker stack"
	@echo "  make test           Run all tests"
	@echo "  make coverage       Run tests with coverage.out"
	@echo "  make coverage-html  Generate coverage.html"
	@echo "  make swagger-check  Validate config/openapi.json"
	@echo "  make smoke          Check health and Swagger endpoints"
	@echo "  make fmt            Format Go files"

env:
	@test -f .env || cp .env.example .env
	@echo ".env is ready"

deps:
	go mod download

db-up:
	docker compose up -d postgres redis minio

db-down:
	docker compose down

dev: env
	go run ./cmd/server

docker-up: env
	docker compose up --build

docker-down:
	docker compose down

test:
	go test ./...

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -n 1

coverage-html: coverage
	go tool cover -html=coverage.out -o coverage.html
	@echo "coverage.html generated"

swagger-check:
	jq empty config/openapi.json
	@echo "Swagger JSON is valid"

smoke:
	curl -fsS http://localhost:8080/health
	@echo ""
	curl -fsS http://localhost:8080/swagger/openapi.json >/dev/null
	@echo "Smoke checks passed"

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')



lint-check:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi
 dev
