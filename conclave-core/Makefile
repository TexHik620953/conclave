.PHONY: build test vet fmt run dev-db up down clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o core ./cmd/core

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Start Postgres and Redis only (for `make run` with a host Go binary).
dev-db:
	docker compose up -d postgres redis

# Start the whole platform in Docker: Postgres + Redis + core (http://127.0.0.1:8090).
up:
	docker compose up -d --build

# Stop the platform (keeps the Postgres volume).
down:
	docker compose down

# Run against the dev database. Override LLM settings via the environment.
run:
	CORE_DB_DSN="postgres://conclave:conclave@localhost:5432/conclave?sslmode=disable" \
	CORE_SECRET_KEY="$${CORE_SECRET_KEY:-dev-secret}" \
	go run ./cmd/core

clean:
	rm -f core
