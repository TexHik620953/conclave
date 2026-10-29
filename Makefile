BIN ?= conclave

.PHONY: all build web run test race vet fmt clean check

all: build

build:
	go build -o $(BIN) ./cmd/conclave

web:
	cd web && npm install && npm run build

run: build
	./$(BIN)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check: test vet web build

clean:
	rm -f $(BIN)
