.PHONY: build build-bin run test test-short test-coverage eval eval-gate lint lint-new lint-new-test package-test compose-check fmt docker clean help

BINARY    := daimon
BUILD_DIR := bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
TAGS      := fts5
COVERAGE  := coverage.out

## build: Build the binary
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 go build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/daimon

## build-bin: Build binary only (alias of build, kept for CI compatibility)
build-bin:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 go build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/daimon

## run: Build and run
run: build
	./$(BUILD_DIR)/$(BINARY) start

## test: Run all tests (with race detector)
test:
	CGO_ENABLED=1 go test -tags "$(TAGS)" ./... -v -race -count=1

## test-short: Run tests without race detector (faster, for dev loops)
test-short:
	CGO_ENABLED=1 go test -tags "$(TAGS)" ./... -v -count=1

## test-coverage: Run tests with coverage profile
test-coverage:
	CGO_ENABLED=1 go test -tags "$(TAGS)" ./... -coverprofile=$(COVERAGE) -covermode=atomic -count=1
	@echo "Coverage report: go tool cover -html=$(COVERAGE)"

## eval: Run the deterministic eval chain over the replay corpus (scorecard + Δ)
eval:
	CGO_ENABLED=1 go run -tags "$(TAGS)" ./evals/cmd/eval

## eval-gate: Hermetic CI gate over checked-in fixtures
eval-gate:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	CGO_ENABLED=1 go run -tags "$(TAGS)" ./evals/cmd/eval \
		-replays "$(CURDIR)/evals/fixtures/replays" \
		-score "$$tmp/score.json" -gate

## eval-calibrate: Score a judge against human labels (LABELS=path [KAPPA=0.6])
eval-calibrate:
	CGO_ENABLED=1 go run -tags "$(TAGS)" ./evals/cmd/calibrate -labels "$(LABELS)" -kappa "$(or $(KAPPA),0.6)"

## lint: Run linter (requires golangci-lint)
lint:
	golangci-lint run ./...

## lint-new: Run linter only on changes since LINT_BASE (requires golangci-lint)
lint-new:
	LINT_BASE_SHA="$(LINT_BASE)" ./scripts/lint-new.sh

## lint-new-test: Test the incremental lint gate
lint-new-test:
	bash scripts/lint-new_test.sh

## package-test: Test native release packaging for the host
package-test:
	bash scripts/package-release_test.sh

## compose-check: Validate Docker Compose configuration
compose-check:
	@created=0; \
	if [ ! -e configs/daimon.yaml ]; then \
		cp configs/daimon.example.yaml configs/daimon.yaml; \
		created=1; \
	fi; \
	trap 'if [ "$$created" = 1 ]; then rm -f configs/daimon.yaml; fi' EXIT; \
	docker compose config --quiet

## arch: Enforce layered dependency direction (blocking gate, mirrors CI)
arch:
	golangci-lint run --enable-only=depguard ./...

## fmt: Format code
fmt:
	go fmt ./...
	goimports -w . 2>/dev/null || true

## vet: Run go vet
vet:
	go vet ./...

## docker: Build Docker image
docker:
	docker build -t $(BINARY):$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		.

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR) $(COVERAGE)

## help: Show this help
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'
