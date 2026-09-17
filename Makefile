.PHONY: help up-deps down-deps ps api api-restart api-logs api-stop \
        migrate-up migrate-down migrate-version \
        migrate-create worker-setup worker-test worker-lint worker-healthcheck \
        attribution drill drill-compare compare parallel-demo test test-live lint verify

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
api: ## 本地运行 Go API (前台, 先 make up-deps)
	cd server && go run ./cmd/api

# 开发期最常踩的坑: 旧实例还占着端口, 新实例启动即失败(日志里是 bind: address already in use),
# 但 curl 打到旧进程上, 表现成"我改的代码没生效"。这个目标把"按端口杀 + 重启 + 等就绪"一次做完。
#
# 两个细节是踩出来的:
# 1) 只杀 -sTCP:LISTEN 的监听者: 直接 `lsof -ti :8080` 会把**连到这个端口的客户端**
#    (浏览器/微信等)也列出来, 照着杀就误伤;
# 2) 启动时把 stdin/stdout/stderr 全部重定向(</dev/null >日志 2>&1): 否则后台进程会继承
#    调用方的管道/终端, "make 结束了但 shell 不返回"就是这么来的。
#
# API 监听端口由环境变量 PORT 决定(默认 8080); 若你改了 PORT, 这里也要跟着传 API_PORT=...
API_PORT ?= 8080

api-restart: ## 重启本地 API(按端口杀旧实例 + 后台起 + 等 healthz): make api-restart [API_PORT=8080]
	@port=$(API_PORT); \
	pids=$$(lsof -nP -ti tcp:$$port -sTCP:LISTEN 2>/dev/null || true); \
	if [ -n "$$pids" ]; then \
	  echo "端口 $$port 上的监听进程:"; ps -o pid=,command= -p $$pids | cut -c1-100; \
	  kill $$pids 2>/dev/null || true; \
	  for _ in 1 2 3 4 5; do sleep 1; lsof -nP -ti tcp:$$port -sTCP:LISTEN >/dev/null 2>&1 || break; done; \
	  pids=$$(lsof -nP -ti tcp:$$port -sTCP:LISTEN 2>/dev/null || true); \
	  if [ -n "$$pids" ]; then echo "仍在监听, 强制结束: $$pids"; kill -9 $$pids 2>/dev/null || true; sleep 1; fi; \
	else echo "端口 $$port 空闲(无需清理)"; fi; \
	( cd server && nohup go run ./cmd/api </dev/null >/tmp/eval-api.log 2>&1 & ); \
	echo "已后台启动, 日志: /tmp/eval-api.log"
	@i=0; while [ $$i -lt 30 ]; do i=$$((i+1)); \
	  if curl -sf -m 2 http://127.0.0.1:$(API_PORT)/healthz >/dev/null 2>&1; then \
	    echo "✅ API 就绪: http://127.0.0.1:$(API_PORT)"; exit 0; fi; \
	  sleep 1; done; \
	echo "❌ 30 秒内未就绪, 日志尾部:"; tail -10 /tmp/eval-api.log; exit 1

api-logs: ## 查看后台 API 日志尾部: make api-logs [N=30]
	tail -n $(or $(N),30) /tmp/eval-api.log

api-stop: ## 停掉本地 API(只杀监听该端口的进程)
	@pids=$$(lsof -nP -ti tcp:$(API_PORT) -sTCP:LISTEN 2>/dev/null || true); \
	if [ -n "$$pids" ]; then kill $$pids 2>/dev/null || true; echo "已停止: $$pids"; else echo "端口 $(API_PORT) 上没有监听进程"; fi

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

# ---- 归因 (M4-3) ----
attribution: ## 重算历史 run 的归因标签: make attribution RUN=112 [APPLY=1]
	@test -n "$(RUN)" || (echo "用法: make attribution RUN=<run_id> [APPLY=1]"; exit 1)
	cd worker && .venv/bin/python -m app.cli.attribution --run-id $(RUN) $(if $(APPLY),--apply,)

# ---- 可靠性演练与一致性校验 (M3) ----
drill: ## 故障演练(默认 v2 60 题; DATASET_ID=3 可跑 30 题快版)
	scripts/fault_drill.sh

drill-compare: ## 故障演练 + 与参照 run 逐题比对: make drill-compare REF=100
	COMPARE_WITH=$(REF) scripts/fault_drill.sh

compare: ## 两次 run 逐题一致性对比: make compare LEFT=100 RIGHT=101
	cd worker && .venv/bin/python -m app.cli.compare_runs --left $(LEFT) --right $(RIGHT)

parallel-demo: ## 并发演示(两个 job 并行) + 产出 A/B 数据
	scripts/parallel_jobs_demo.sh

# ---- 全量收口 ----
test: ## 运行全部单测 (Go + Python + Web)
	cd server && go test ./...
	cd worker && .venv/bin/pytest -q
	cd web && pnpm test

lint: ## 静态检查 (go vet + ruff)
	cd server && go vet ./...
	cd worker && .venv/bin/ruff check .

test-live: ## 集成测试(连真实 PG/Qdrant/embedding; 需 .env 配好 key)
	cd server && RUN_LIVE=1 go test ./internal/store/ -v
	cd worker && RUN_LIVE=1 .venv/bin/pytest -q

verify: ## 一键全量验证 (起依赖 + 迁移 + 测试 + lint)
	@make up-deps
	@make migrate-up
	@make test
	@make lint
	@echo "提示: 集成测试请单独跑 make test-live(RUN_LIVE=1, 需要 .env 里的 key)"

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