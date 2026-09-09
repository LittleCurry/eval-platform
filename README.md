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

前置：Docker Desktop、Go 1.24+、Python 3.11+（开发机用 3.14）、Node 20+（M0-6 起需要）。

```bash
cp .env.example .env        # 可选，不复制则用默认值
make up-deps                # 启动 postgres + qdrant
make migrate-up             # 建库表结构到最新
make api                    # 起 Go API → http://localhost:8080/healthz