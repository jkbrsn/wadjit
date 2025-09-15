.PHONY: explain test lint default

.DEFAULT_GOAL := explain

explain:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Options for test targets:"
	@echo "  [N=...]  - Number of times to run burst tests (default 1)"
	@echo "  [RACE=1] - Run tests with race detector"
	@echo "  [V=1]    - Add V=1 for verbose output"
	@echo ""
	@echo "Targets:"
	@echo "  test             - Run tests (unit tests using cache)."
	@echo "  lint             - Run golangci-lint."
	@echo "  explain          - Display this help message."

# Number of times to run burst tests, default 1
N ?= 1

TEST_FLAGS :=
ifdef RACE
	TEST_FLAGS += -race
endif
ifdef V
	TEST_FLAGS += -v
endif

test:
	@echo "==> Running tests"
	@go test -count=$(N) $(TEST_FLAGS) ./...

lint:
	@echo "==> Running linter (golangci-lint)"
	@golangci-lint run
