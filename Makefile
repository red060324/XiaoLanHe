GO_PACKAGES ?= ./...
WEB_DIR := frontend/xiaolanhe-web
BASE_REF ?= origin/master
LIGHTRAG_COMPOSE_FILE ?= deploy/docker-compose.lightrag.yml

.PHONY: fmt-check vet test test-race eval web-test web-build hooks architecture spec-drift verify ci integration-live docker-build middleware-config middleware-up middleware-down mysql-static mysql-live redis-live rocketmq-live milvus-static milvus-live fence-static fence-live lightrag-static lightrag-config lightrag-bootstrap-empty lightrag-up lightrag-down lightrag-live lightrag-lifecycle

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

verify: fmt-check vet test eval web-test hooks architecture mysql-static lightrag-static web-build

ci: fmt-check vet test test-race eval web-test hooks architecture mysql-static lightrag-static spec-drift web-build

# CI invokes this only after provisioning the pinned real services. Every target
# below requires its connection variables and fails instead of silently skipping.
integration-live: mysql-live redis-live rocketmq-live lightrag-live

docker-build:
	docker build -t xiaolanhe:local .

middleware-config:
	docker compose -f deploy/docker-compose.middleware.yml config --quiet

middleware-up:
	docker compose -f deploy/docker-compose.middleware.yml up --detach --wait

middleware-down:
	docker compose -f deploy/docker-compose.middleware.yml down

mysql-static:
	@go test -count=1 ./internal/adapter/mysql ./internal/adapter/mysqltx ./internal/account/repository/mysql ./internal/assistant/repository/mysql ./internal/catalog/repository/mysql ./internal/community/repository/mysql ./internal/flashsale/repository/mysql ./internal/order/repository/mysql ./internal/promotion/repository/mysql ./migrations/mysql

mysql-live:
	@test -n "$$XLH_MYSQL_TEST_DSN" || { echo "XLH_MYSQL_TEST_DSN is required" >&2; exit 2; }
	@go test -count=1 -v ./internal/adapter/mysql -run '^TestMySQLMigrationIntegration$$'

redis-live:
	@test -n "$$XLH_TEST_REDIS_URL" || { echo "XLH_TEST_REDIS_URL is required" >&2; exit 2; }
	@go test -count=1 -v ./internal/flashsale/repository/redis -run 'Integration$$'

rocketmq-live:
	@test -n "$$XLH_TEST_ROCKETMQ_NAMESERVERS" || { echo "XLH_TEST_ROCKETMQ_NAMESERVERS is required" >&2; exit 2; }
	@test -n "$$XLH_TEST_ROCKETMQ_BROKER_ADDR" || { echo "XLH_TEST_ROCKETMQ_BROKER_ADDR is required" >&2; exit 2; }
	@go test -count=1 -v ./internal/flashsale/repository/rocketmq -run '^TestRocketMQTransactionCommitAndRollbackIntegration$$'

milvus-static:
	@bash deploy/check-lightrag-compose.sh
	@bash -n deploy/check-lightrag-milvus.sh
	@go test -count=1 ./deploy
	@python3 -m unittest deploy/lightrag/test_init_milvus.py

milvus-live:
	@bash deploy/check-lightrag-milvus.sh "$(LIGHTRAG_COMPOSE_FILE)" "$${COMPOSE_PROJECT_NAME:-}"

fence-static:
	@bash -n deploy/check-lightrag-fence.sh deploy/lightrag-bootstrap-empty.sh deploy/lightrag-up.sh deploy/lightrag-contract-hash.sh
	@python3 -m unittest deploy/lightrag/test_rebuild_fence.py deploy/lightrag/test_guarded_start.py

fence-live:
	@test -n "$$XLH_LIGHTRAG_REBUILD_FENCE_DIR" || { echo "XLH_LIGHTRAG_REBUILD_FENCE_DIR is required" >&2; exit 2; }
	@test -n "$$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" || { echo "XLH_LIGHTRAG_DEPLOYMENT_GENERATION is required" >&2; exit 2; }
	@test -n "$$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256" || { echo "XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256 is required" >&2; exit 2; }
	@bash deploy/check-lightrag-fence.sh "$$XLH_LIGHTRAG_REBUILD_FENCE_DIR" "$$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"

lightrag-static: milvus-static fence-static
	@bash -n deploy/check-lightrag-live.sh deploy/check-lightrag-lifecycle.sh

lightrag-config: lightrag-static
	@test -n "$$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" && test "$${XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR#/}" != "$$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" || { echo "XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR must be absolute" >&2; exit 2; }
	@test -n "$$XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR" && test "$${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR#/}" != "$$XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR" || { echo "XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR must be absolute" >&2; exit 2; }
	docker compose --profile bootstrap -f $(LIGHTRAG_COMPOSE_FILE) config --quiet

lightrag-bootstrap-empty: lightrag-static
	@bash deploy/lightrag-bootstrap-empty.sh "$(LIGHTRAG_COMPOSE_FILE)"

lightrag-up: lightrag-static
	@bash deploy/lightrag-up.sh "$(LIGHTRAG_COMPOSE_FILE)" "$${COMPOSE_PROJECT_NAME:-}"

lightrag-down:
	docker compose -f $(LIGHTRAG_COMPOSE_FILE) down

lightrag-live: milvus-live fence-live
	@bash deploy/check-lightrag-live.sh http://127.0.0.1:9621 "$$XLH_LIGHTRAG_API_KEY" "$$XLH_LIGHTRAG_REBUILD_FENCE_DIR" "$$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"

lightrag-lifecycle:
	@bash deploy/check-lightrag-lifecycle.sh
