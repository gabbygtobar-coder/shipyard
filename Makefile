GO ?= go
BIN_DIR := bin
export PATH := $(shell $(GO) env GOPATH)/bin:$(PATH)

.PHONY: build test test-integration lint run compose clean cover docker

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/shipyardd ./cmd/shipyardd
	$(GO) build -trimpath -o $(BIN_DIR)/shipyard ./cmd/shipyard

test:
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...

test-integration:
	SHIPYARD_INTEGRATION=1 $(GO) test -race -count=1 -timeout 15m -run TestIntegrationHello ./internal/runtime/

compose:
	docker compose up --build

cover: test
	$(GO) tool cover -func=coverage.out

lint:
	$(GO) vet ./...
	golangci-lint run ./...

run:
	$(GO) run ./cmd/shipyardd

docker:
	docker build -t shipyardd:dev .

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
