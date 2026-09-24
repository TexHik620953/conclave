.PHONY: build test vet fmt run clean

build:
	go build -o agent ./cmd/agent

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Run against a local core. Provide tokens explicitly.
#   make run CORE=http://127.0.0.1:8090 DEVICE=cc_... USER=cu_... WORKSPACE=$PWD
CORE ?= http://127.0.0.1:8090
DEVICE ?=
USER ?=
WORKSPACE ?= .

run:
	go run ./cmd/agent --core-url "$(CORE)" --device-token "$(DEVICE)" --user-token "$(USER)" --workspace "$(WORKSPACE)"

clean:
	rm -f agent
