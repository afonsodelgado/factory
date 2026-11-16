.PHONY: build test lint dev go-build go-test go-lint web-build web-test web-lint web-typecheck web-dev pwa-build pwa-test pwa-lint pwa-typecheck pwa-dev pwa-embed

build:
	$(MAKE) web-build pwa-build
	$(MAKE) pwa-embed
	$(MAKE) go-build

test: go-test web-test pwa-test

lint: go-lint web-lint web-typecheck pwa-lint pwa-typecheck

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
		pnpm exec turbo run $(@:web-%=%) --filter=@factory/web; \
	fi

pwa-build pwa-test pwa-lint pwa-typecheck pwa-dev:
	@if [ ! -f apps/pwa/package.json ]; then \
		printf 'apps/pwa/package.json is absent; skipping pwa %s\n' '$(@:pwa-%=%)'; \
	else \
		pnpm exec turbo run $(@:pwa-%=%) --filter=@factory/pwa; \
	fi

pwa-embed:
	@if [ ! -f apps/pwa/package.json ]; then \
		printf 'apps/pwa/package.json is absent; skipping pwa embed\n'; \
	else \
		node apps/pwa/scripts/embed.mjs; \
	fi
