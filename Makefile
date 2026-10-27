SHELL := /bin/bash

.PHONY: fmt fmt-check test vet check docs-install docs-dev docs-check docs-build docs-preview

fmt:
	@gofmt -w $$(find api pkg cmd -name '*.go')

fmt-check:
	@test -z "$$(gofmt -l .)"

test:
	@go test ./...

vet:
	@go vet ./...

check: fmt-check test vet

docs-install:
	@cd docs && bun install --frozen-lockfile

docs-dev:
	@cd docs && bun run dev

docs-check:
	@cd docs && bun run check

docs-build:
	@cd docs && bun run build

docs-preview:
	@cd docs && bun run preview
