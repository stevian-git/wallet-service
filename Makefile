SHELL := /bin/bash

-include .env
export

POSTGRES_PORT ?= 5436

DATABASE_URL ?= postgres://ledger_app:app_dev_pw@localhost:$(POSTGRES_PORT)/wallet?sslmode=disable
API_KEY ?= dev_api_key
MIGRATE_DATABASE_URL ?= pgx5://ledger_migrator:migrator_dev_pw@localhost:$(POSTGRES_PORT)/wallet?sslmode=disable&default_query_exec_mode=simple_protocol

.PHONY: up down reset migrate migrate-down migrate-version test run build fmt vet

up:
	docker compose up -d --wait postgres

down:
	docker compose down

reset:
	docker compose down -v

migrate: up
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate up

migrate-down: up
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate down 1

migrate-version: up
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate version

test:
	go test ./... -count=1

run: up
	DATABASE_URL='$(DATABASE_URL)' API_KEY='$(API_KEY)' go run ./cmd/api

build:
	go build -o bin/api ./cmd/api
	go build -o bin/migrate ./cmd/migrate

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...
