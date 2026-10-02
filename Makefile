# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
BINARY_NAME=cmdcode2api
BINARY_UNIX=$(BINARY_NAME)_unix

# Version stamped into the binary (overridable: make build VERSION=v1.2.3).
VERSION?=dev
LDFLAGS=-s -w -X cmdcode2api/internal/app.Version=$(VERSION)

all: test build

build:
	$(GOBUILD) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) -v ./cmd/cmdcode2api

test:
	$(GOTEST) -count=1 ./...

# The full local gate: vet first, then tests. -race needs cgo, so it is left to
# CI (which has a C toolchain) rather than failing on a plain Windows host.
check:
	$(GOCMD) vet ./...
	$(GOTEST) -count=1 ./...

cover:
	$(GOTEST) -count=1 -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -func=coverage.out

clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_UNIX)
	rm -f coverage.out

run: build
	./$(BINARY_NAME)

# Cross compilation
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_UNIX) -v ./cmd/cmdcode2api

.PHONY: all build test check cover clean run build-linux
