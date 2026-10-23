.DEFAULT_GOAL := help
.PHONY: lint fmt test test-offline test-integration test-intergration clean tidy vuln modernize-check audit test/cover help

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## fmt: format code
fmt:
	golangci-lint fmt ./...

## test: run tests with race detector
test:
	go test -race -shuffle=on -coverprofile=coverage.out -covermode=atomic ./...

## test-offline: run tests with the synthetic Codex process, without a model/backend
test-offline:
	PATH="$(CURDIR)/scripts/test-fixtures:$(PATH)" $(MAKE) test

## test-integration: run integration tests
test-integration:
	go test -race -tags=integration -timeout=120s -v ./integration/...

## test-intergration: backwards-compatible alias for misspelled target
test-intergration: test-integration

## clean: remove build artifacts
clean:
	rm -f coverage.out

## tidy: tidy go modules
tidy:
	go mod tidy

## vuln: run govulncheck
vuln:
	go tool govulncheck ./...

## modernize-check: preview Go modernizations without changing files
modernize-check:
	go fix -n ./...

## audit: run all checks
audit: lint test vuln modernize-check
	go mod tidy -diff
	go mod verify

## test/cover: open HTML coverage report
test/cover: test
	go tool cover -html=coverage.out

## help: show this help
help:
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'
