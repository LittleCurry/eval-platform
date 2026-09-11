.PHONY: help up-deps down-deps ps api migrate-up migrate-down migrate-version \
        migrate-create worker-setup worker-test worker-lint worker-healthcheck \
        drill drill-compare compare test lint verify

help: ## 显示可用目标
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F'## ' '{split($$1, t, ":"); printf "  \033[36m%-18s\033[0m %s\n", t[1], $$2}'

# ---- 依赖服务 (docker compose) ----
up-deps: ## 启动依赖服务 (postgres + qdrant)
	docker compose up -d

down-deps: ## 停止并移除依赖服务容器
	docker compose down

ps: ## 查看依赖服务状态
	docker compose ps

# ---- Go API ----
api: ## 本地运行 Go API (先 make up-deps)
	cd server && go run ./cmd/api

# ---- 数据库迁移 (golang-migrate) ----
-include .env
POSTGRES_USER     ?= eval
POSTGRES_PASSWORD ?= eval_dev_password
POSTGRES_DB       ?= eval_platform
PG_PORT           ?= 5432
PG_DSN            ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:$(PG_PORT)/$(POSTGRES_DB)?sslmode=disable
MIGRATE           ?= $(HOME)/go/bin/migrate

migrate-up: ## 应用全部迁移到最新
	$(MIGRATE) -path server/migrations -database "$(PG_DSN)" up

migrate-down: ## 回滚最近一个迁移
	$(MIGRATE) -path server/migrations -database "$(PG_DSN)" down 1

migrate-version: ## 查看当前迁移版本
	$(MIGRATE) -path server/migrations -database "$(PG_DSN)" version

migrate-create: ## 新建迁移: make migrate-create NAME=add_users
	@test -n "$(NAME)" || (echo "用法: make migrate-create NAME=xxx"; exit 1)
	$(MIGRATE) create -ext sql -dir server/migrations -seq "$(NAME)"

# ---- Python worker ----
PYTHON ?= /usr/local/bin/python3

worker-setup: ## 创建 worker venv 并安装依赖(含 dev)
	$(PYTHON) -m venv worker/.venv
	cd worker && .venv/bin/pip install -U pip
	cd worker && .venv/bin/pip install -e ".[dev]"

worker-test: ## 运行 worker 单元测试
	cd worker && .venv/bin/pytest -q

worker-lint: ## ruff 静态检查
	cd worker && .venv/bin/ruff check .

worker-healthcheck: ## worker 依赖连通性自检
	cd worker && .venv/bin/python -m app.healthcheck

# ---- 可靠性演练与一致性校验 (M3) ----
drill: ## 故障演练(默认 v2 60 题; DATASET_ID=3 可跑 30 题快版)
	scripts/fault_drill.sh

drill-compare: ## 故障演练 + 与参照 run 逐题比对: make drill-compare REF=100
	COMPARE_WITH=$(REF) scripts/fault_drill.sh

compare: ## 两次 run 逐题一致性对比: make compare LEFT=100 RIGHT=101
	cd worker && .venv/bin/python -m app.cli.compare_runs --left $(LEFT) --right $(RIGHT)

# ---- 全量收口 ----
test: ## 运行全部单测 (Go + Python + Web)
	cd server && go test ./...
	cd worker && .venv/bin/pytest -q
	cd web && pnpm test

lint: ## 静态检查 (go vet + ruff)
	cd server && go vet ./...
	cd worker && .venv/bin/ruff check .

verify: ## 一键全量验证 (起依赖 + 迁移 + 测试 + lint)
	@make up-deps
	@make migrate-up
	@make test
	@make lint

# ---- Web (前端) ----
.PHONY: web-install web web-build web-test

web-install: ## 安装前端依赖 (pnpm install)
	cd web && pnpm install

web: ## 前端开发服务器 (vite, 默认 :5173)
	cd web && pnpm dev

web-build: ## 前端生产构建 (type-check + 打包)
	cd web && pnpm build

web-test: ## 前端单测 (vitest)
	cd web && pnpm test