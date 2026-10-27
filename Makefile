SHELL := /bin/bash

-include .env
export

POSTGRES_PORT ?= 5436
KAFKA_PORT ?= 9092

DATABASE_URL ?= postgres://ledger_app:app_dev_pw@localhost:$(POSTGRES_PORT)/wallet?sslmode=disable
API_KEY ?= dev_api_key
MIGRATE_DATABASE_URL ?= pgx5://ledger_migrator:migrator_dev_pw@localhost:$(POSTGRES_PORT)/wallet?sslmode=disable&default_query_exec_mode=simple_protocol
KAFKA_BROKERS ?= localhost:$(KAFKA_PORT)
KAFKA_TOPIC ?= ledger.events.v1
KAFKA_CONSUMER_GROUP ?= ledger-consumer-demo
HOLD_SWEEP_INTERVAL ?= 5s
HOLD_SWEEP_BATCH_SIZE ?= 100
OUTBOX_BATCH_SIZE ?= 50
OUTBOX_POLL_INTERVAL ?= 1s
OUTBOX_MAX_BACKOFF ?= 30s
RECONCILER_POLL_INTERVAL ?= 5s
VUS ?= 10
DURATION ?= 30s
BASE_URL ?= http://localhost:8080

.PHONY: up up-db up-kafka up-observability down reset migrate migrate-down migrate-version test run relay consumer-demo holdsweeper reconciler mock-settlement kafka-topic build fmt vet loadtest loadtest-hot chaos

up: up-db up-kafka up-observability

up-db:
	docker compose up -d --wait postgres

up-kafka:
	docker compose up -d --wait redpanda

up-observability:
	docker compose up -d jaeger prometheus grafana

loadtest:
	mkdir -p results
	BASE_URL='$(BASE_URL)' API_KEY='$(API_KEY)' VUS='$(VUS)' DURATION='$(DURATION)' k6 run deploy/k6/wallet.js

loadtest-hot:
	mkdir -p results
	BASE_URL='$(BASE_URL)' API_KEY='$(API_KEY)' VUS='$(VUS)' DURATION='$(DURATION)' HOT=1 k6 run deploy/k6/wallet.js

chaos: build up
	./scripts/chaos.sh

down:
	docker compose down

reset:
	docker compose down -v

migrate: up-db
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate up

migrate-down: up-db
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate down 1

migrate-version: up-db
	MIGRATE_DATABASE_URL='$(MIGRATE_DATABASE_URL)' go run ./cmd/migrate version

test:
	go test ./... -count=1

run: up-db
	DATABASE_URL='$(DATABASE_URL)' API_KEY='$(API_KEY)' go run ./cmd/api

relay: up
	DATABASE_URL='$(DATABASE_URL)' KAFKA_BROKERS='$(KAFKA_BROKERS)' KAFKA_TOPIC='$(KAFKA_TOPIC)' \
	OUTBOX_BATCH_SIZE='$(OUTBOX_BATCH_SIZE)' OUTBOX_POLL_INTERVAL='$(OUTBOX_POLL_INTERVAL)' OUTBOX_MAX_BACKOFF='$(OUTBOX_MAX_BACKOFF)' \
	go run ./cmd/relay

consumer-demo: up-kafka
	KAFKA_BROKERS='$(KAFKA_BROKERS)' KAFKA_TOPIC='$(KAFKA_TOPIC)' KAFKA_CONSUMER_GROUP='$(KAFKA_CONSUMER_GROUP)' \
	go run ./cmd/consumer-demo

holdsweeper: up-db
	DATABASE_URL='$(DATABASE_URL)' HOLD_SWEEP_INTERVAL='$(HOLD_SWEEP_INTERVAL)' HOLD_SWEEP_BATCH_SIZE='$(HOLD_SWEEP_BATCH_SIZE)' \
	go run ./cmd/holdsweeper

reconciler: up-db
	DATABASE_URL='$(DATABASE_URL)' RECONCILER_POLL_INTERVAL='$(RECONCILER_POLL_INTERVAL)' \
	go run ./cmd/reconciler

mock-settlement: up-db
	DATABASE_URL='$(DATABASE_URL)' API_KEY='$(API_KEY)' go run ./cmd/mock-settlement $(MOCK_OUTPUT) $(MOCK_DATE)

kafka-topic: up-kafka
	docker compose exec redpanda rpk topic create '$(KAFKA_TOPIC)' -p 3 -r 1 || true

build:
	go build -o bin/api ./cmd/api
	go build -o bin/migrate ./cmd/migrate
	go build -o bin/relay ./cmd/relay
	go build -o bin/consumer-demo ./cmd/consumer-demo
	go build -o bin/holdsweeper ./cmd/holdsweeper
	go build -o bin/reconciler ./cmd/reconciler
	go build -o bin/mock-settlement ./cmd/mock-settlement

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...
