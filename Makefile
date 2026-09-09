.PHONY: help up-deps down-deps ps

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up-deps: ## 启动依赖服务 (postgres + qdrant)
	docker compose up -d

down-deps: ## 停止并移除依赖服务容器
	docker compose down

ps: ## 查看依赖服务状态
	docker compose ps

# ---- 数据库迁移 (golang-migrate) ----
-include .env                       # 若存在 .env 则读入, 允许覆盖下面默认值
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