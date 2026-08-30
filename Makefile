GO_PACKAGES ?= ./...
WEB_DIR := frontend/xiaolanhe-web
BASE_REF ?= origin/master

.PHONY: fmt-check vet test test-race web-test web-build hooks architecture spec-drift verify ci docker-build

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal -type f -name '*.go'))"; \
	if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

vet:
	go vet $(GO_PACKAGES)

test:
	go test -count=1 $(GO_PACKAGES)

test-race:
	go test -race -count=1 $(GO_PACKAGES)

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

verify: fmt-check vet test web-test hooks architecture web-build

ci: fmt-check vet test test-race web-test hooks architecture spec-drift web-build

docker-build:
	docker build -t xiaolanhe:local .
