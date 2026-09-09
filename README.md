# Eval Platform — LLM 自动化评测平台

自研核心评测逻辑的 LLM 应用质量底座：上传中文评测集，对内置或外部接入的 RAG/Agent 链路做批量异步评测，
输出检索/生成两侧可信指标、可复现的 A/B 实验对比与 Bad Case 标注闭环。

## 技术栈

Go (Gin) + Python worker + PostgreSQL + Qdrant + Redis(可选) + Vue3，Docker Compose 编排。

## 目录速览

| 目录 | 内容 |
|------|------|
| `server/` | Go API（业务 CRUD、run 编排、报告查询） |
| `worker/` | Python 评测执行（检索/judge/指标） |
| `web/` | 前端控制台 |
| `datasets/` | 评测集资产（语料/题目） |
| `docs/` | ADR、部署、故障演练 |
| `process.md` | 完整任务规划与决策记录（唯一执行依据） |

## 快速开始

前置：Docker Desktop、Go 1.24+、Python 3.11+（开发机 3.14）、Node 20+（`web/` 需要，配 pnpm）。

```bash
cp .env.example .env        # 可选，不复制则用默认值
make up-deps                # 启动 postgres + qdrant
make migrate-up             # 建库表结构到最新
make api                    # 起 Go API → http://localhost:8080/healthz
```

另一个终端：

```bash
make worker-setup           # 首次：建 venv 装依赖
make worker-healthcheck     # worker 侧连通性自检
make web-install && make web   # 前端首次安装并起 dev server → http://localhost:5173
```

## 开发循环

```bash
make test       # 全部单测 (Go + Python + Web)
make lint       # go vet + ruff
make verify     # 一键全量验证 (起依赖 + 迁移 + 测试 + lint)
make help       # 查看全部命令
```

## 状态

- M0：环境与骨架 ✅（2026-09-09）