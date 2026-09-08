.PHONY: help up-deps down-deps ps

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up-deps: ## 启动依赖服务 (postgres + qdrant)
	docker compose up -d

down-deps: ## 停止并移除依赖服务容器
	docker compose down

ps: ## 查看依赖服务状态
	docker compose ps