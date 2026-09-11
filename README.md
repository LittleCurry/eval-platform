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

## 可靠性设计（M3）

评测任务异步化：`POST /api/v1/runs` 只落库入队并立即返回，执行交给 Python worker。

- **逐条 checkpoint**：每完成一条 case，在**同一事务**里写 `case_results` + 推进 `job_items` + 重算 `jobs.progress`。
  崩溃点落在事务内则重跑该条，落在事务间则已完成部分不重算。
- **僵尸任务接管**：worker 定期心跳；心跳超时的任务由 `reclaim_stale_jobs`（或 `POST /api/v1/jobs/reclaim`）
  把在跑条目放回队列，任意 worker 重新领取后**只补剩余**。
- **幂等**：`case_results` 有 `UNIQUE (run_id, case_id)` + `ON CONFLICT DO UPDATE`
  → 执行语义是 at-least-once，**结果语义是 exactly-once**，指标不会被重复累加。
- **重试与死信**：单条失败按 `retry_count` 重试（默认 3 次），超限进 `failed`（死信）并记录原因，
  不阻塞其余条目；最终 `run.status=failed` 且 `error` 说明失败条数。
- **进度可观测**：`GET /runs/:id/progress` 返回 job 进度与"疑似掉线"标记；前端仅在任务活跃时每 3s 轮询（D13）。

实测：60 题跑一半 `kill -9` worker → 重启续跑，崩溃时已完成 4 条、续跑只处理 56 条，最终逐题与不中断跑一致。

```bash
make drill-compare REF=100   # 故障演练 + 与参照 run 逐题比对(退出码 0 即通过)
make parallel-demo           # 两个 job 并行(并发粒度 D12) + 产出 A/B 数据
make compare LEFT=100 RIGHT=101
```

细节见 `docs/reliability.md`，实测记录与已知边界见 `docs/fault-drills.md`。

## 开发循环

```bash
make test       # 全部单测 (Go + Python + Web)
make lint       # go vet + ruff
make verify     # 一键全量验证 (起依赖 + 迁移 + 测试 + lint)
make test-live  # 集成测试(连真实 PG/Qdrant/embedding, 需 .env 里有 key)
make help       # 查看全部命令
```

## 状态

- M0：环境与骨架 ✅（2026-09-09，tag `v0.1.0-m0`）
- M1：语料/数据集/case 管理（中文语料 21 篇 + 60 题评测集）✅（2026-09-09）
- M2：内置检索评测闭环 ✅（2026-09-10，tag `v0.2.0-m2`）—— 基线 Recall@5 91.4% / MRR@5 83.4% / Hit@5 100%
- M3：异步任务基建（队列 / checkpoint 续跑 / 心跳接管 / 重试死信 / 进度可视化）✅（2026-09-11，tag `v0.3.0-m3`）
  —— v2 60 题基线 Recall@5 94.9% / MRR@5 88.4% / Hit@5 100%