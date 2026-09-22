.PHONY: build web web-check test vet fmt clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Build the Vue frontend into internal/web/dist (embedded by the Go server).
web:
	cd web && npm install && npm run build

# Type-check the frontend without emitting.
web-check:
	cd web && npm install && npm run typecheck

# Build the binary (includes the frontend).
build: web
	go build -ldflags "$(LDFLAGS)" -o conclave ./cmd/conclave

# Go-only build without rebuilding the frontend.
build-go:
	go build -ldflags "$(LDFLAGS)" -o conclave ./cmd/conclave

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f conclave
