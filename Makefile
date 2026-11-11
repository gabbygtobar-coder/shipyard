GO ?= go
BIN_DIR := bin
export PATH := $(shell $(GO) env GOPATH)/bin:$(PATH)

.PHONY: build test test-integration lint run compose clean cover docker aws-up aws-down

TF_DIR := infra/aws

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

# aws-up creates the cheap dev stack. aws-down deletes ECS services first, then
# the stack. Both ask Terraform to confirm. See docs/aws-setup.md.
aws-up:
	terraform -chdir=$(TF_DIR) init -input=false
	terraform -chdir=$(TF_DIR) apply -input=false

aws-down:
	terraform -chdir=$(TF_DIR) init -input=false
	$(TF_DIR)/empty-cluster.sh
	terraform -chdir=$(TF_DIR) destroy -input=false

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
