.PHONY: build web test vet fmt clean

# Build the Vue frontend into internal/web/dist (embedded by the Go server).
web:
	cd web && npm install && npm run build

# Build the binary (includes the frontend).
build: web
	go build -o conclave ./cmd/conclave

# Go-only build without rebuilding the frontend.
build-go:
	go build -o conclave ./cmd/conclave

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f conclave
