.PHONY: build test lint dev go-build go-test go-lint web-build web-test web-lint web-typecheck web-dev

build: go-build web-build

test: go-test web-test

lint: go-lint web-lint web-typecheck

dev: go-build
	$(MAKE) web-dev

go-build:
	go build ./...

go-test:
	go test ./...

go-lint:
	go vet ./...

web-build web-test web-lint web-typecheck web-dev:
	@if [ ! -f apps/web/package.json ]; then \
		printf 'apps/web/package.json is absent; skipping web %s\n' '$(@:web-%=%)'; \
	else \
		pnpm exec turbo run $(@:web-%=%); \
	fi
