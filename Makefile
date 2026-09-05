GO_PACKAGES ?= ./...
WEB_DIR := frontend/xiaolanhe-web
BASE_REF ?= origin/master

.PHONY: fmt-check vet test test-race eval web-test web-build hooks architecture spec-drift verify ci docker-build middleware-config middleware-up middleware-down lightrag-static lightrag-config lightrag-up lightrag-down lightrag-live lightrag-lifecycle

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal -type f -name '*.go'))"; \
	if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

vet:
	go vet $(GO_PACKAGES)

test:
	go test -count=1 $(GO_PACKAGES)

test-race:
	go test -race -count=1 $(GO_PACKAGES)

eval:
	go run ./cmd/eval-assistant

web-test:
	npm --prefix $(WEB_DIR) test

web-build:
	npm --prefix $(WEB_DIR) run build

hooks:
	@bash -n .hooks/*.sh
	@.hooks/check-text-basics.sh $$({ git diff --name-only $(BASE_REF)...HEAD 2>/dev/null; git diff --name-only; git ls-files --others --exclude-standard; } | sort -u)
	@.hooks/check-doc-links.sh $$(find . -name '*.md' -not -path './.git/*' -not -path './frontend/xiaolanhe-web/node_modules/*')
	@.hooks/check-no-placeholders.sh $$(find cmd internal frontend/xiaolanhe-web/src .github -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.yml' -o -name '*.yaml' \))

architecture:
	.hooks/check-architecture.sh

spec-drift:
	.hooks/check-spec-drift.sh $(BASE_REF)

verify: fmt-check vet test eval web-test hooks architecture lightrag-static web-build

ci: fmt-check vet test test-race eval web-test hooks architecture lightrag-static spec-drift web-build

docker-build:
	docker build -t xiaolanhe:local .

middleware-config:
	docker compose -f deploy/docker-compose.middleware.yml config --quiet

middleware-up:
	docker compose -f deploy/docker-compose.middleware.yml up --detach --wait

middleware-down:
	docker compose -f deploy/docker-compose.middleware.yml down

lightrag-static:
	@bash deploy/check-lightrag-compose.sh
	@bash -n deploy/check-lightrag-live.sh deploy/check-lightrag-lifecycle.sh

lightrag-config: lightrag-static
	docker compose -f deploy/docker-compose.lightrag.yml config --quiet

lightrag-up:
	docker compose -f deploy/docker-compose.lightrag.yml up --detach --wait

lightrag-down:
	docker compose -f deploy/docker-compose.lightrag.yml down

lightrag-live:
	@bash deploy/check-lightrag-live.sh http://127.0.0.1:9621 "$$XLH_LIGHTRAG_API_KEY"

lightrag-lifecycle:
	@bash deploy/check-lightrag-lifecycle.sh
