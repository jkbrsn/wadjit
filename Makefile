.PHONY: explain test test-race vet lint default

.DEFAULT_GOAL := explain

explain:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  test             - Run tests (unit tests using cache). Add V=1 for verbose output."
	@echo "  test-race        - Run unit tests for race conditions. Add V=1 for verbose output."
	@echo "  vet              - Run go vet."
	@echo "  lint             - Run golangci-lint."
	@echo "  explain          - Display this help message."

TEST_FLAGS :=
# Flag V=1 for verbose mode
ifdef V
	TEST_FLAGS += -v
endif

test:
	@echo "==> Running tests..."
	@go test $(TEST_FLAGS) ./...

test-race:
	@echo "==> Running tests with race detector..."
	@go test -race $(TEST_FLAGS) ./...

vet:
	@echo "==> Running go vet..."
	@go vet ./...

lint:
	@echo "==> Running linter (golangci-lint)..."
	@golangci-lint run ./... || echo "Linting failed or golangci-lint not found. Consider installing it: https://golangci-lint.run/usage/install/"
