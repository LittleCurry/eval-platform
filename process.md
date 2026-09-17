# LLM 自动化评测平台（Eval Platform）— 从 0 到生产级任务规划

> 本文档是项目的**唯一执行规划**，随进展持续更新。核心用法见第 10 节「与 agent 的协作方式」：
> 动手时由 agent 基于本文档产出细粒度「任务卡」，你按卡实现，卡点找 agent 解决。
> 每次改动必须：**先写/改测试 → 测试通过 → 提交一个 git commit**（AGENTS.md 约定）。

---

## 目录

1. [定位、范围与已定决策](#1-定位范围与已定决策)
2. [总体架构与技术选型](#2-总体架构与技术选型)
3. [核心领域模型与数据表](#3-核心领域模型与数据表)
4. [关键设计决策（先想清楚再做）](#4-关键设计决策先想清楚再做)
5. [评测集建设指南（中文自建语料）](#5-评测集建设指南中文自建语料)
6. [里程碑规划 M0–M7](#6-里程碑规划-m0m7)
7. [目录结构与提交规范](#7-目录结构与提交规范)
8. [风险与对策](#8-风险与对策)
9. [开工前待拍板清单](#9-开工前待拍板清单)
10. [与 agent 的协作方式](#10-与-agent-的协作方式)

---

## 1. 定位、范围与已定决策

**一句话定位**：自研核心评测逻辑的 LLM 应用质量底座 —— 上传中文评测集（question + golden 锚点），对内置或外部接入的 RAG / Agent / LLM 链路做**批量异步评测**，输出检索与生成两侧的可信指标、可复现的实验对比和 Bad Case 标注闭环。

### 已定决策（来自需求讨论）

| # | 决策 | 对架构的影响 |
|---|------|--------------|
| D-A | 评测集为**中文自建领域语料** | 规划评测集建设流程（§5）；UI 与标注界面为中文。**语料已定 = 本地 mock 中文语料《知简 CRM》帮助文档体系**（`datasets/corpus/`，虚构 SaaS 产品，21 篇带 doc_id 的 .md，可整体替换为真实语料，见 §9 Q1） |
| D-B | 评测平台 + **被测系统通过配置接入** | 核心抽象为统一 Case Runner + Adapter 双模式（内置链路 / 外部 HTTP 服务），Judge 恒在平台内（§4 D3） |
| D-C | 前端不做产品生态，但**要美观整洁、同事可共用** | 需要登录 + RBAC + 项目数据隔离 + UI 设计基线（§4 D9、M7） |
| D-D | **模型供应商：先接 DeepSeek（已有 key），架构上支持多厂商切换，并按成本/延迟/难度自动路由** | 抽象 chat/embed/rerank 三类供应商接口 + 路由策略层（§4 D11）；DeepSeek 无 embedding/rerank 接口 → M2 检索侧需第二家供应商（待申请）；judge 单次 run 内固定模型保证可比 |

### 边界（明确不做 / 注意）

- **不套开源评测框架核心逻辑**（DeepEval / RAGAS 等）：指标计算、judge prompt 与评分协议、claim 分解全部自研；开源框架仅作调研，结论写入 README「参考调研」章节。
- 被测的 RAG **不是**本平台要交付的产品；平台提供内置可调链路用于 A/B，同时留外部接入能力。
- UI 追求美观可用，但不做营销页、国际化等产品化功能。
- LLM/embedding/reranker **供应商与预算待定**（见 §9），代码按 OpenAI 兼容接口设计，保证可切换 DeepSeek / Qwen / GLM / vLLM 本地等。

---

## 2. 总体架构与技术选型

### 架构图

```
┌────────────────────────────────────────────────────────────────────┐
│  Web (Vue3 + TS + Naive UI/Element Plus + ECharts)                  │
│  数据/语料管理 · 评测任务 · 报告 · 对比实验 · Bad Case 标注 · 登录     │
└───────────────────────────────┬────────────────────────────────────┘
                                │ HTTP + WS(进度推送)   [JWT]
┌───────────────────────────────▼────────────────────────────────────┐
│  Go API (Gin)                                                      │
│  auth/RBAC · 语料/数据集 · run/任务编排 · 结果查询 · 标注            │
└───────────────┬──────────────────────────────┬─────────────────────┘
                │ 读/写                        │ 领任务(轮询)
┌───────────────▼───────────────┐   ┌──────────▼──────────────────────┐
│ PostgreSQL                    │   │ Python Worker (评测执行)          │
│ 业务数据 + 可靠任务队列(SKIP   │   │ retrieval runner · claim 分解 ·    │
│ LOCKED) · job_items 级 checkpoint│ │ judge · adapter(builtin/http)    │
└───────────────▲───────────────┘   └──┬──────────┬──────────┬─────────┘
                │                      │          │          │
        ┌───────┴────────┐   ┌─────────▼──┐  ┌────▼────┐  ┌──▼──────────┐
        │ Qdrant (向量)   │   │ LLM API    │  │ Reranker │  │ 被测 RAG/Agent│
        │ chunks + payload│  │ (judge/生成)│  │ (可选)   │  │ (可选, HTTP)  │
        └────────────────┘   └────────────┘  └─────────┘  └─────────────┘
```

Redis 定位：**可选**（judge 缓存读多写少的旁路、并发限流计数、进度推送）。不承担队列可靠性。

### 选型与理由

| 组件 | 选择 | 理由 |
|------|------|------|
| 后端 | Go + Gin | 高并发 API 与异步编排的舒适区；部署单二进制 |
| 评测执行 | Python 3.11+ | LLM 生态、judge/指标代码迭代快 |
| 存储 | PostgreSQL（业务 + 队列） | 任务状态/结果/队列一处持久化，满足「中断恢复」 |
| 向量库 | Qdrant | 简历认可度最高的开源向量库，docker 一键起，payload 过滤强 |
| 队列 | PG `FOR UPDATE SKIP LOCKED`（不用 Redis 队列） | 见 §4 D2 —— 可靠性是简历卖点 |
| LLM 客户端 | OpenAI 兼容 SDK | 供应商可切换 |
| 前端 | Vue3 + TS + Vite + Naive UI + ECharts | 中文生态文档好、上手快、好看省力 |
| 部署 | Docker Compose（单机/内网） | M0 就起；同事共用 = 内网一键部署 |
| 迁移 | golang-migrate（PG schema） | 版本化、可回滚 |

### 开发方式约定

- 本地开发：Go / Python / Node 在宿主机跑，`docker compose` 只起 **postgres + qdrant**（+ 可选 redis）。Makefile 提供 `make up-deps / make api / make worker / make web / make migrate / make test`。
- 每个服务独立目录，互不 import 对方代码，只通过 **HTTP API + DB 表** 通信。
- 评测执行链路（Python）里不写任何"业务 CRUD"，业务 CRUD 不写评测逻辑。

### 本机环境基线（已核验 ✅）

- 宿主机：macOS 13.7.8（Intel）｜Docker 27.3.1 + Compose v2.30.3｜Go 1.24.13｜Node v26.8.1 + pnpm 12.1.0｜Xcode CLT｜Homebrew。
- 容器镜像已预拉取：`postgres:16-alpine`、`qdrant/qdrant:v1.19.1`（**compose 固定此版本，qdrant 版本标签带 `v` 前缀**，勿用 latest 裸标签）。
- Python：worker 用 `python3.14.7`（python.org 官方安装器，`/usr/local/bin/python3`，pip 26.2.1 + venv 正常；项目内建 venv）。migrate 已装（`go install`，位于 `~/go/bin/migrate`，在 `~/.zshrc` PATH 内；`--version` 显示 `dev` 因无 tag，功能正常，如需正式版本号可 `brew install golang-migrate`）。
- **国内网络注意**：直连 Docker Hub 会超时/被 reset，本机处理方案（M7 部署文档要提醒同事机器照做）：
  1. `~/.docker/daemon.json` 配镜像加速（dockerproxy.net / docker.1ms.run / daocloud / 1panel.live 置前，失效阿里云兜底）；
  2. **Go 写的 docker CLI 不读 macOS 系统代理** → 已在 `~/.zshrc` 追加 `https_proxy/http_proxy/all_proxy=127.0.0.1:7897`（含 no_proxy 排除镜像域名与 localhost；备份 `~/.zshrc.bak-20260908`）；
  3. **`docker login` ✅ 已打通（2026-09-08）**：根因是 Docker Desktop 新版把 login 收敛到 daemon 的 token 流程，daemon 在 VM 内出网被墙，`daemon.json` 的 proxies 与 CLI 环境变量均无效。正解 = Docker Desktop 设置里开 Manual 代理，且 **HTTP 与 HTTPS 两个框都要填** `http://127.0.0.1:7897`（对应 settings-store.json 的 `ProxyHTTPMode=manual` + `OverrideProxyHTTP` + `OverrideProxyHTTPS`，键值均为 URL 字符串——headless 写入时键名必须照此，否则不生效/崩溃）。登录已验证 `Login Succeeded`；
  4. 凭证安全：Docker Hub 账号/密码若曾在对话或日志出现，用后尽快改密或改用 PAT（`docker login -u 用户名 --password-stdin`）。

---

## 3. 核心领域模型与数据表

> 表结构在 M1 落地第一版，后续里程碑按需加列/加表，禁止猜一堆用不上的字段。
> chunk 向量本体在 Qdrant；PG 只存 chunk 的元信息（映射关系、chunking 指纹）。

| 表 | 用途 | 关键字段 |
|----|------|----------|
| `users` | 登录、角色（M7-1 落地） | email(唯一, 强制小写), name, password_hash(bcrypt), role(admin/editor/viewer), disabled, last_login_at |
| `projects` | 数据隔离与归属单位（M7-2 落地；**全员可见**，见 D24） | name(唯一), description, created_by → users(id) |
| `corpora` | 语料库 | project_id, name, source_type |
| `documents` | 语料原文（清洗后） | corpus_id, title, raw_text, meta jsonb |
| `chunk_meta` | chunk 元信息（向量在 Qdrant） | doc_id, chunk_idx, char_start, char_end, chunking_hash, qdrant_point_id |
| `datasets` | 评测集 | project_id, name, description |
| `cases` | 单条测试用例 | dataset_id, qid, question, gold_anchors jsonb, reference_answer, category, difficulty, notes |
| `pipeline_profiles` | 评测链路配置模板（被测"配置"） | project_id, name, config jsonb（见 §4 D3/D7）, config_hash |
| `runs` | 一次实验 | project_id, profile_id, dataset_id, config_snapshot jsonb, config_hash, git_sha, status, stats jsonb |
| `jobs` | run 的执行实例 | run_id, status, progress jsonb, heartbeat_at |
| `job_items` | **case 级 checkpoint**（续跑单元） | job_id, case_id, status, retry_count, last_error, result_id |
| `case_results` | 单 case 评测结果 | run_id, case_id, retrieved jsonb, answer, claims jsonb, judge jsonb, metrics jsonb, latency_ms, tokens jsonb, flags jsonb |
| `claims` | 答案分解出的原子断言与判定（可用 jsonb 先顶着，量大再拆表） | case_result_id, text, verdict(supported/unsupported/irrelevant), evidence |
| `annotations` | Bad Case 人工标注（挂在 **run + case** 上，M6 落地） | run_id, case_id, status(open/fixed/verified/wontfix), reason(retrieval/hallucination/generation/dataset/unknown), comment, assignee, created_by；**唯一键 (run_id, case_id)** |
| `human_gold_scores` | 人工金标（校准 judge 用，M6 落地） | run_id, case_id, annotator, verdict(faithful/hallucinated/unclear), relevance/helpfulness(1–5 可空), note, reviewed；**唯一键 (run_id, case_id, annotator)** |
| `judge_cache` | judge 输出缓存（省钱） | cache_key_hash, provider/model/prompt_version, output jsonb |

**必须记录的审计字段**：`created_at / updated_at / created_by`（涉及 `runs/cases/annotations` 等所有用户可写表）。

---

## 4. 关键设计决策（先想清楚再做）

> 每条对应一个面试可讲五分钟的工程点。里程碑里会逐条落实。

### D1 检索金标用「稳定锚点」，不用 chunk id

chunk id 在切分参数变化后失效 → chunk size 就没法作为实验变量。方案：
- 标注时 golden 指向**文档内稳定区间**：`{"doc": "d01", "span": "条款3.2 违约责任", "offset": [100, 320]}`（段落/小节级即可，不必精确到字符）。
- 每次 run 按当前 chunking 配置，把 anchor 区间**映射为覆盖它的 chunk 集合** → 当题的 `G`。
- 校验：锚点必须能被映射到至少一个 chunk，映射失败在导入期就报错。
- 好处：chunk size / embedding / reranker / top-k **全部**可作实验变量，gold 永不失效。

### D2 队列：PG `SELECT … FOR UPDATE SKIP LOCKED` + case 级 checkpoint

- 队列就是 `jobs` + `job_items` 两张表：worker 轮询可领取的 job → 按 job_items 逐条执行。
- **边跑边提交**：每条 case 完成即写 `case_results` + 更新 `job_items.status`（独立事务）→ worker 被杀也只损失当前一条，重启续跑跳过已完成项。这是"中断恢复"的实锤实现。
- 心跳 `heartbeat_at` 超时 → 任务可被别的 worker 接管（幂等：以 job_items 状态为准）。
- 失败重试：LLM/网络瞬时错误按类型退避重试（`retry_count`），超过上限进 dead-letter（job 标记 failed + 记录原因），不吞错。
- Redis 只做可选旁路，**可靠性不依赖 Redis**。
- 并发控制：worker 级并发上限 +（可选）按 LLM 供应商的令牌桶限流。

### D3 被测系统接入：统一 Case Runner + Adapter 双模式

一次评测 run 的链路 = **Retrieval → (可选) Generate → Judge**。Judge 永远平台内（自研）。
被测链路通过 `pipeline_profiles` 的配置声明，两种 mode：

- **`builtin`（内置，平台自己跑）**：用于 A/B 实验。配置含语料库、chunking、embedding 供应商/模型、top-k、reranker 开关与模型、生成模型与 prompt。实验变量全可调。
- **`http`（外部被测服务）**：跑线上 RAG/Agent 的回归。配置含 endpoint、鉴权、请求/响应字段映射。平台按契约调用并取回 `answer + retrieved contexts`，与内置链路进**同一套指标/judge/对比/标注体系**。

外部 HTTP Adapter 契约（先固定下来，避免后面返工）：

```jsonc
// 平台 → 被测服务
{ "question": "…", "config": { "top_k": 5, "options": {} } }
// 被测服务 → 平台（映射后落 case_results）
{
  "answer": "…",
  "contexts": [ { "doc_id": "d01", "chunk_id": "c-xxx", "text": "…", "score": 0.91 } ],
  "latency_ms": 1200,
  "usage": { "input_tokens": 300, "output_tokens": 150 }
}
```

被测服务返回的 context 缺失/为空 → 该 case 的生成评测标记为"无检索上下文"，归因时算检索侧问题，不硬评。

### D4 幻觉检测 = claim 级事实核查，不做"整段幻觉分"

1. **claim 分解**：judge 把生成答案分解为原子断言列表（自写 prompt + JSON 输出协议）。
2. **逐条验证**：每条 claim 对照**该 case 实际检索到的上下文**做 entailment 判定：`supported / unsupported / irrelevant`（判定协议与 rubric 自研，可 judge 或小型 NLI 模型，模式可配）。
3. 输出指标：claim 支持率、幻觉率（unsupported/总数），并**返回被判定为幻觉的具体 claim 文本与依据** → 直接进 Bad Case 展示，能告诉开发"哪句是编的"。

### D5 Judge 本身要可信、可复现

- 调用固定 temperature=0，记录 judge 的 provider/model/prompt 版本到 run 配置快照。
- judge 输出强约束：仅允许合法 JSON（协议字段校验，非法重试 N 次后记 failed）。
- 按 `(输入, judge模型, prompt版本)` 哈希缓存结果（`judge_cache`），避免重跑烧钱。
- 小规模**人工金标集**（几十条）→ 算 judge 与人工的一致性（Cohen's κ / 一致率），报告里展示"自动评测可信度"。
- pairwise 对比时**顺序随机化 / 双向各评一次**缓解位置偏差；pointwise 时评分标准 rubric 写死在 prompt 里。
- 用中文评测时，judge prompt 用中文编写并在小样本上校准过分数刻度。

### D6 对比实验讲统计，不止两个平均分

- **同题 paired**：A/B 跑同一评测集，输出 per-question 指标差值表与分布。
- 显著性：Wilcoxon signed-rank（scipy 一行），报告标注 p 值。
- **分层结论**：按 `category / difficulty / flag` 分组看"谁在哪些题上赢/输"，而不是一个笼统结论。
- 导出：差值表 + 汇总报告（Markdown/CSV/JSON 三选一导出）。

### D7 实验可复现 = 配置快照 + 指纹 + 代码版本

- `config_snapshot jsonb` 记录**全量**配置：chunking(策略/大小/overlap/分隔符)、embedding(供应商/模型/维度/批大小)、reranker(开关/模型)、检索(top_k/阈值)、生成(供应商/模型/温度/prompt id)、judge(同左)。
- `config_hash = sha256(规范化 config_snapshot)` → 同配置可定位历史 run。
- 每次 run 记录 `git_sha`（执行时代码版本）→ README 写明"代码 + 配置 + 数据 + judge 版本四件套齐备才可复现"。

### D8 成本与规模控制

- 大批量前先跑 5–10 条小批量 smoke run 校验链路。
- judge 结果缓存（D5）+ 按 case 记录 token 用量 → 报告可算单 run 成本。
- worker 并发上限可配；对 LLM 失败按类型退避，避免打爆供应商限流。
- LLM 相关 API key 只进 `.env`（不入库、不入 git）；外部服务 adapter 的密钥同规则。

### D9 多用户与共享（同事共用）

- **JWT 登录 + RBAC 三角色**：`admin`（全量管理）/ `editor`（建语料、数据集、跑 run、标注）/ `viewer`（只看报告）。
- **项目（project）数据隔离**：语料/数据集/run/标注都属于某 project；默认创建者可见，admin 可加成员。避免同事互相覆盖实验。
- 界面设计基线（M7 统一）：一致的间距/字号/色彩体系、空状态、加载态、错误提示（toast）、表格分页与列配置、深浅主题可选；ECharts 报告配色与品牌色一致。

### D10 指标口径（先定死公式，避免各算各的）

检索侧（每题计算后取均值；`G`=该题 gold 锚点映射出的 chunk 集，`Rk`=检索 top-k）：

| 指标 | 定义 |
|------|------|
| Recall@k | `|G ∩ Rk| / |G|` |
| Precision@k | `|G ∩ Rk| / k` |
| MRR@k | 首个命中 chunk 的 reciprocal rank，无命中记 0 |
| Hit@k | 是否至少命中 1 个 gold chunk |

生成侧（judge，自研协议）：claim 支持率、幻觉率（D4）、answer relevance / helpfulness（rubric 打分或 pairwise）、faithfulness vs reference（有参考答案时可选）。

**归因规则**（区分"召回问题 vs 幻觉问题"的核心）：
- gold chunk 未进 top-k → **retrieval_miss**；
- gold 进了但答案含 unsupported claim → **hallucination**；
- 检索正常、无幻觉但 relevance/helpfulness 低 → **generation_quality**；
- 外部服务没返回 context → **context_missing**。
每个 case 依此自动打 `flag`，供报告过滤与标注工作台入口。

### D11 模型供应商抽象与自动路由（多厂商可切换）

目标：先接 DeepSeek 跑通，架构上支持多家 LLM 按 **成本 / 延迟 / 难度** 自动切换，后续申请到新 key 即插即用。

- **三类供应商接口**（OpenAI 兼容为主，个别厂商做薄适配层）：
  - `chat`：DeepSeek（`deepseek-chat` 默认、`deepseek-reasoner` 用于高难度），后续 Qwen/GLM/vLLM 本地等；
  - `embed`：DeepSeek **不提供**，M2 前需第二家（候选：SiliconFlow bge-m3、阿里云 text-embedding 等）；
  - `rerank`：可选，与 embed 同理走支持 rerank 的厂商。
- **供应商注册表**：`pipeline_profiles` 配置里声明 provider（base_url/api_key 引用/模型名/温度等），API key 只存 `.env`。
- **路由策略（后期增强，先固定后演进）**：按任务类型（生成/judge/embed）声明候选池 + 规则（成本优先/延迟优先/按 case.difficulty 或题长选模型）。路由命中结果写入 run 快照与 case_results，保证可复盘。
- **可比性红线**：**judge 模型与 prompt 在单次 run 内固定**，不做逐题路由——跨 run 对比时 judge 必须同一模型+prompt 版本（D5/D7）。路由主要用于**被测生成链路**（模拟生产模型路由）及低难度大批量场景，且要能在报告里看出路由选择。

---

### D12 并发粒度 = job 级（一个 job 只能被一个 worker 持有）

**决策**：M3 的并发模型是「**一个 job 一个 worker，多 job 并行**」，不做「多 worker 分食同一 job」。语义由 `jobs.status` 单值守护 + `SELECT … FOR UPDATE SKIP LOCKED` 保证：领取即置 `running`，之后任何领取路径（按 id 或泛化）都拿不到它。

- **为什么这样定**：checkpoint 的正确性依赖"这个 job 只有一个写者"。若允许多 worker 分食，就必须给 `job_items` 加 `worker_id` + 租约过期 + 续租失败回收，否则会出现两个 worker 各算一遍同一条 case（`job_items` 行数虽不翻倍，但 `case_results` 会重复累加、指标被污染，且**不报错**）。在单 job 题量 ≤ 数百、单 job 时长 ≤ 分钟内时，这个复杂度换不来收益。
- **并行的正确用法**：一次评测 = 一个 job；要做大吞吐就**并发提交多个 run**（多 job），各自有独立 worker。
- **可执行断言**（回归防线）：
  - Go：`TestQueueRunningJobIsNeverClaimedTwiceLive`（已被持有的 job 二次领取返回 nil；4 协程争抢同一 pending job 有且仅有一个成功）、`TestQueueConcurrentClaimNoDuplicateLive`（多 job 并发领取互不重复）；
  - worker：`worker/tests/test_concurrency_live.py`（4 条：运行中不可再领、并发争抢唯一胜出、两个 job 可被两个 worker 同时持有、微任务批次不重叠）。
- **backlog（附触发条件）**：item 级租约（`job_items.worker_id` + 租约过期 + 续租）。**触发条件**：单 job 题量 > 500，或单 job 时长 > 10 min，或需要"大 job 不浪费机器"时再实现。
- **实测证据**（2026-09-11，`scripts/parallel_jobs_demo.sh`）：两个 run 各 60 题同时提交，两个 worker 进程各持一个 job → 采样 32 次中 **15 次观测到两者同时 running**，两者均 `succeeded`；不同 `top_k` 的两次 run 因 `config_hash` 不同被 `compare_runs` 正确判为"不可比（A/B 场景）"。

---

### D13 进度推送 = 短轮询（3s，仅活跃任务）；WebSocket/SSE 列 backlog

**决策**：M3 的进度可见性用 `GET /runs/:id/progress` + 前端 3s 短轮询实现，**不引入 WebSocket/SSE**。轮询仅在列表/详情页存在**活跃任务**（`pending`/`running`）时启动，空闲即停（前端有"自动刷新"开关）。

- **为什么轮询够了**：进度是数据库里的一行 JSON（`jobs.progress`，由 checkpoint 事务用 `count(*)` 重算，见 `docs/reliability.md` §3），单次查询是主键索引命中；活跃任务期间 3s 一次的代价可忽略，而空闲时**零请求**。
- **被否决的"实时推送"**：WebSocket 要引入连接管理、心跳、断线重连、多实例广播（Redis pub/sub），在"单机 docker compose + 同事共用"的形态下收益不足；SSE 稍轻但仍需长连接与代理配置。
- **backlog（附触发条件）**：需要**实时日志面板**（逐条 case 的滚动日志、worker 侧事件流）或进度写入频率提升到秒级以下时，再评估 SSE/WebSocket —— 顺序上先 SSE（单向、可复用 HTTP 鉴权）。这是 M7 可视化阶段的评估项，不影响 M4/M5。
- **一致性说明**：`stale` 标记（疑似 worker 掉线）也走同一接口，因此"进度卡住"与"worker 掉线告警"是同一个信号，不需要额外通道。

---

### D14 生成评测的配置必须进快照，且"只在启用时写入"

**决策**：M4 起 `config_snapshot` 增加 `generation` 段（`provider/base_url/model/prompt_id/temperature/max_tokens/max_context_chars`），但**仅当该 run 启用了生成评测时才写入**；`POST /api/v1/runs` 带 `generation` 对象 = 启用，不带 = 只跑检索（历史行为）。worker **是否生成、用什么参数，一律以快照为准**，`--generate` 这类 CLI 开关已移除。

- **为什么"只在启用时写入"**：无条件加字段会让**全部历史 run 的 `config_hash` 作废**——M2/M3 的 run #3/#22/#32/#99/#100 那条"同一 `config_hash` 跨代码版本指标逐位一致"的证据、以及 `compare_runs` 的"是否同一配置"判定都会断掉。改为条件写入后，两边都成立：只跑检索的指纹**字节级不变**（红线由 `snapshot_test.go` 的 `wantConfigHash` + `test_snapshot.py` 的 `WANT_CONFIG_HASH_BASELINE` 双向锁定），而"启用生成"是一枚**可区分的新指纹**（这本身就是另一场实验，理应不同）。
- **为什么快照是唯一权威**：如果参数来自 worker 的环境变量，报告里记录的模型/prompt 就可能与实际调用不一致——"可复现"变成空话。实测教训：S4 阶段用 CLI 开关临时跑的两个 run（#139/#140）与 run #100 的 `config_hash` **完全相同**（`4e767020`），却是两种不同的实验；这正是本决策要消除的歧义。
- **跨语言指纹的浮点约定**：`temperature` 是首个进快照的浮点数，而 Go 把 `0.0` 编成 `0`、Python 的 `json` 编成 `0.0` —— 同配置会算出不同指纹。约定为**整数化浮点写成整数**（两侧各有 `normalizeFloat` / `normalize_float`），并用两组常量锁定（`temperature=0` 与 `temperature=0.3`，后者证明不是"只对了整数"的巧合）。
- **服务端与 worker 的分工**：服务端只校验**形状**（范围、必填、正数），`GENERATION_*` 环境变量在两侧都作为"提交时未提供字段的默认值"；prompt 文件是否存在由 worker 在**开始跑题之前**校验并让任务快速失败（`GenerationSectionError` / `PromptError` → job failed，不刷死信）。
- **生成元信息落在哪**：`case_results.answer` + `case_results.generation`（与检索结果**同一事务**写，继承 checkpoint 幂等），其中 `prompt_hash` 是 prompt **内容**指纹（服务端拿不到文件内容，故快照里只有 `prompt_id`，内容哈希由 worker 记录）——这也是 M4-2 judge 缓存键（D5/D8）的前半截。

---

### D15 归因规则与阈值进 `runs.metrics`，不进 `config_snapshot`

**决策**：M4-3 起 `runs.metrics.attribution` 记录 `{version, scope, k, low_rank_limit, low_rank_ratio, quality_line}`；**归因的任何参数都不写入配置快照**，也不参与 `config_hash`。

- **为什么不进快照**：快照进指纹（正是 D14 的反面）。若把 `attribution` 塞进快照，全部历史 run 的 `config_hash` 立刻作废，M2 以来"同 `config_hash` 跨代码版本指标逐位一致"的复现证据、以及 `compare_runs` 的"是否同一配置"判定会一起断掉。改口径是**重新解释既有事实**，不是换配置——它不该改变"这次实验是什么"的指纹。
- **为什么必须记下来**：标签数依赖 k 与阈值，不记就无法跨 run 比较。实测：run #112（k=1）`retrieval_partial=12`，run #100/#155（k=5）只有 5 —— 同一批题、同一套规则，只因 k 不同就差了 7 条；`low_rank_limit` 在 k=1 时是 1（**永不触发**）、k=5 时是 3。
- **为什么 `version` 也要记**：谓词或优先级一变，历史标签的含义就变了；`version` 是"这行标签按哪版规则打出来的"唯一凭据。
- **`scope` 表达覆盖范围**：只跑检索的 run 是 `retrieval`（判定类标签一律不出），启用判定的是 `retrieval+judge`。**"没有判定"绝不能等价于"没有幻觉"**——这是 M4-2 rubric v1 负控（全编造答案拿到 5/5）换来的教训。
- **阈值可覆盖，但用哪个记哪个**：`--quality-line` / `--low-rank-ratio` 允许探索（把达标线从 3 放到 4，run #155 立刻从 0 条质量标签变 10 条），但**用哪个值就必须记哪个值**，否则跨 run 的标签数不可比。

---

### D16 `flags` = 环节事实标签集，`flags[0]` 即主因（不新增列）

**决策**：`case_results.flags` 是**多标签**的"环节事实"集合，数组顺序 = 固定优先级（上游 / 更严重者在前）；报告侧取 `flags[0]` 当主因，**不新增 `primary_flag` 列**，也不再单独落一份标签计数。

- **为什么用"事实"而不是"责任"**：run #155 里 `retrieval_partial` 命中 5 题，而这 5 题**都没导致错答**（144 条断言全部 supported）——它描述"检索没做全"，不等于"这题答错了"。两类语义混成一列，报告就没法既报"风险"又报"责任"。
- **为什么不在库里存主因**：主因是**规则优先级 + 当次阈值**的函数。存下来就多一个真相源，规则升级后必须全库回填；而"顺序即优先级"让任何读取方零成本推导。
- **为什么计数派生而不落库**：`ListRunFlagCounts`（Go 侧 `jsonb_array_elements_text` 聚合）随查随算；重算 CLI 只改 `flags`，不会出现"计数与明细不一致"的过期数据。
- **优先级表**：`no_gold > anchor_incomplete > retrieval_miss > retrieval_partial > retrieval_low_rank > hallucination > off_topic > generation_quality > no_claims`。两条容易被问：① `anchor_incomplete` 压过 `retrieval_miss` —— 没召回 gold 但断言全部有据，说明**锚点漏标**（数据问题），该修数据集而不是检索；② `generation_quality` 的前置是无幻觉且证据到位 —— 否则一个坏答案会在"幻觉"和"质量"两栏各计一次。
- **`context_missing` 缺席（原计划里有）**：数据集锚点已验证 100% 覆盖，它在数据层**没有可计算谓词**；两种真实含义已被 `retrieval_miss`（系统没召回）与 `no_gold`/`anchor_incomplete`（标注问题）覆盖。要保留"语料里根本没这条知识"的语义，正确做法是标注 `answerable:false` 的无答案样本（`gold_anchors: []`），那时它才可计算（列入 v3）。

---

### D17 检索上下文正文**按需从向量库取**，不落库

**决策**：报告抽屉要展示"检索到的原文 vs 答案"，正文通过 `GET /api/v1/runs/:id/cases/:case_id/context` **实时**从 Qdrant 取；`case_results.retrieved` 继续只存 `point_id/doc_id/score`，**不把 chunk 正文复制进 PG**。

- **为什么不落库**：落库等于把语料正文在 PG 与向量库各存一份（**双写**）。两边一旦不一致（重切分、重索引、手工修文档），报告会拿旧正文解释新检索，而"报告说的"与"检索实际用的"不一致正是本项目最忌讳的失真。项目已有的两条同类决策（D14 快照为唯一权威、D15 口径不重复落库）都是这个取向。
- **历史 run 为什么也能取到**：集合名 = `corpus{id}_{切分指纹前 8 位}`（Go 侧 `eval.CollectionName` 与 worker 侧 `collection_name` 同一约定，且被单测锁定为 `corpus4_5f45e034`），而 `corpus_id` 在 run 上、切分配置在快照里 —— 两者都不随时间变化，所以 run #155 现在仍能取回它当时那份上下文，**不需要重跑**。
- **降级而不是报错**：向量库不可用 / 集合缺失 / 快照缺 `chunking` 段时，接口返回 **200 + `error` 字段**，`chunks` 里 `found=false`；前端退化成"只列 point_id/doc_id/score"的骨架并提示原因。抽屉正文是锦上添花，不该让整份报告 5xx。
- **为什么不做成"只存摘要哈希"之类的折中**：正文的用途就是给人看（判断"这句是不是在编"），任何压缩/摘要都会让证据失真；而按需取的代价只有一次 Qdrant 往返（5 条 point，实测毫秒级）。

---

### D18 对比结论 = 三条证据并排，而不是一行 p 值

**决策**：A/B 报告同时给三样，缺一不可 ——
① **逐题差值**（paired，D6）② **Wilcoxon signed-rank p 值**（**自研**：并列取平均秩 + 小样本 DP 精确分布 + 大样本并列校正的正态近似；scipy 只作测试期的可选交叉验证）③ **翻转题清单**（A 坏→B 好 / A 好→B 坏，逐题列出标签迁移）。

- **为什么不把 p 值当结论**：检索指标是**离散且大量并列**的（recall 常只有 0/0.5/1，60 题里 55 题满分），Wilcoxon 的正态近似在大量 ties 下不可靠。所以结论以"噪声底（M3 实测 2.1e-3）+ 翻转题清单"为准，p 值仅作旁证 —— 这句话必须写进界面，否则同事会把 p 值当圣旨。
- **为什么不引 scipy**：worker 运行时依赖刻意只有 5 个，为一条统计引入 numpy+scipy（~40MB）不划算；公式短、可用手算样例锁定（`[1..5]` → W+=15、p=0.0625），并在测试里用 scipy 交叉验证（缺库则 skip）。
- **噪声底是"最小可觉差"**：差值落在噪声底内一律判"无法区分"、界面涂灰，既不算改善也不算恶化 —— 这一步是防止把跨 run 抖动报告成"优化有效"的唯一硬约束。
- **指标有方向**：幻觉率/无关率**越低越好**，改善/恶化计数与配色都按方向判定（`higher_is_better`），否则"幻觉率下降"会被涂成红色。

---

### D19 A/B 计算放 Go，导出放前端

**决策**：对比计算在 `server/internal/eval/ab.go`（纯函数）+ `GET /api/v1/compare`；CSV/Markdown/JSON 导出由前端用**同一份响应**生成，服务端不写第二套格式化。worker 的 `compare_runs.py` 保持"一致性校验"定位不动。

- **为什么计算放 Go**：报告页本来就是 Go 直连，前端要能即时换 run 重看；更要紧的是**统计只有一份实现**，不会出现"CLI 说显著、页面说不显著"。
- **为什么导出放前端**：接口已经返回结构化结果，三种格式都只是它的视图；服务端再实现一遍等于给自己埋一个"两处口径不一致"的隐患。
- **为什么不动 `compare_runs.py`**：它是**一致性校验**（故障演练用），跨配置时按设计拒绝 —— 它自己就会提示"请用 M5 的报告工具"。两个工具各管一件事，不抢。

---

### D20 人工标注与金标都挂在 **run** 上，不挂 dataset/case

**决策**：`annotations` 唯一键是 `(run_id, case_id)`，`human_gold_scores` 唯一键是 `(run_id, case_id, annotator)`。两者都记 run，而不是只记 case。

- **为什么**：judge 判的是**某一次 run 产出的那份答案**——换一次 run（top_k / 模型 / 温度变了）答案就变了。拿"题级"金标去校准某次 run 的 judge 输出，比的是两个不同对象；同理，"这题修好了没"也必须指定"相对哪一次 run"。
- **代价与接受理由**：换 run 要重标，标注成本不跨 run 复用。接受——因为**复用会制造错误结论**，而错误结论比多标一遍贵得多。
- **annotator 进唯一键**：双人独立打分的意义就是"金标本身可信吗"（M6 校准报告里单独给 inter-annotator agreement）。若系统替人编一个默认标注员，两份打分会被合成一条，双人一致性永远算不出来。

### D21 标注状态机在服务端，字段更新一律"指针语义"

**决策**：`open→fixed→verified` 的流转规则写在 Go（`canTransition`），DB 的 CHECK 只管枚举；所有"可写部分字段"的接口（`PATCH /annotations/:id`、`POST /annotations`、`/human-gold` 的 POST/PATCH）统一 **没传 = 不改，显式空串 = 清空**；分数用 **传 0 = 撤回（置 NULL）** 表示清空。

- **为什么状态机不放 DB 约束**：规则会变（"verified 不能降级"这类业务判断），改规则不该写迁移；而且前端绕不过去——服务端是唯一入口。
- **为什么要指针语义**：工作台/打分页的交互是"点一下改一个字段"。零值当"没传"会**静默清空**没提交的字段——M6-1 真机踩过一次（只改状态，统计里冒出一批 `unclassified`），`human_gold_scores` 的 upsert 又踩过一次（只改判词，把上次打的分数和备注冲成 NULL/空串，校准样本凭空消失）。两次都是"接口调用方看不出异常"的那种坏。因此 upsert 冲突时按 `COALESCE(EXCLUDED.x, 原值)` **合并**，而不是整行覆盖。
- **为什么分数要能撤回**：合法分数是 1–5，0 不是分数——正好可以拿 0 当"清空"信号。否则打错一个分只能删整条记录重来，连带把 verdict 一起丢掉。

---

### D22 认证用 bcrypt + 无状态 JWT(HS256)，首用户 bootstrap 成管理员

**决策**：口令 `bcrypt`（`x/crypto`）；token 用 `golang-jwt/jwt/v5` 签 **HS256**，有效期 12 小时；**没有 refresh token、没有会话表、不做服务端撤销**；系统空库时第一个注册者自动成为 admin，之后注册关闭（加人走管理员）。

- **为什么无状态而不是会话表**：单实例内部工具，会话表带来的收益（可撤销、可看在线）远小于它带来的运维面（清理、并发、多实例共享）。代价说清楚：**签出去的 token 到期前撤不回来**，只能换 `JWT_SECRET` 让全部失效。
- **但"停用账号"必须立即生效**：所以每个请求都回库确认账号仍存在且未停用（`service.Authenticate`）。这是"角色可以等 token 过期、停用不能等"的取舍 —— 同事离职当天必须立刻断掉。
- **为什么只认 HS256**：`WithValidMethods` 把算法白名单钉死。否则 `alg=none`（无签名）与 `alg=RS256用公钥当HMAC密钥` 这两种经典攻击都能得手，测试里各有一条。
- **为什么要校验 iss/aud**：只验签名等于"只要是本密钥签的就认"，同一个密钥的另一个服务的 token 就能横向过来。
- **口令为什么拒绝 >72 字节**：bcrypt 只吃前 72 字节，更长的口令会被**静默截断**（"100 字节口令"等于"它的前 72 字节"）。宁可报错，也不接受一个被剪短的口令。
- **为什么首用户自动 admin**：否则部署完谁也进不去，只能手动插库。**不做自助注册**：内部工具里"谁能进来"该由管理员决定，而不是"谁先看到地址谁进"。
- **token 存 localStorage 的代价**：前后端分离 + 无状态 JWT，token 必须由 JS 取出来塞进 `Authorization`。代价是 XSS 能偷 token —— 因此前端不用 `v-html` 渲染用户内容，后端响应里**绝不带** `password_hash`（Go 侧 `json:"-"`，TS 类型里干脆没这个字段）。

### D23 权限 = 5 个动作 × 3 个角色，路由按权限分组

**决策**：动作收敛成 `read / write / submit / delete / admin`，角色是 `viewer / editor / admin`；`server/internal/auth/rbac.go` 是唯一真相源，前端 `utils/session.ts` 照抄同一张表。路由按**权限**分组挂中间件（`read / write / submit / admin / delete` 五个 gin group），不按资源分组。

- **为什么删数据单独一档**：不可逆动作单独给 admin，日常干活的人（editor）不需要它 —— "能改"和"能删"是两种信任级别。
- **为什么提交实验（`submit`）单独一档**：它会真花钱调 LLM。能看能改的人未必该能随便烧钱。
- **为什么路由按权限分组**：这样"这个端点谁能用"在路由表上一眼可见，新加端点必须选一个组；漏选不会被静默放过 —— `TestEveryAPIRouteRequiresToken` 会把路由表里每个非公开端点打一遍，要求无 token 时全是 401。
- **401 与 403 严格分开**：401 = 没登录/失效（前端跳登录页并带回跳地址）；403 = 登录了但没权限（前端**留在原地**说明缺什么权限）。混用会让"权限不足"表现成反复跳登录页，用户永远不知道自己只是没权限。
- **前端的权限只负责"不让人白点"**：菜单按角色裁剪、路由守卫拦角色不足的页面、禁掉自己改自己的角色选择。真正的边界在服务端 —— 两边各有一份实现且有各自的测试。

---

### D24 项目不建成员表：`created_by` 留痕 + 全员可见，隔离靠"项目内一致性"

**决策**：项目**不做成员表、不做按人可见的权限**。所有登录用户都能看到所有项目；项目解决的是另外两件事：
① **数据不串台** —— 语料/数据集/run 都归属某个项目，页面看的是"当前项目"的东西；
② **归属留痕** —— 谁建的项目/数据集/语料写进 `created_by`（列表带出 `owner_email`），出事找得到人。

隔离的硬规则只有一条，写在**创建 run 的同一个事务**里：**一次 run 的 `dataset_id` 与 `corpus_id` 必须同属一个项目**，显式传的 `project_id` 也必须与两者一致，否则 `ErrProjectMismatch` → 400。

- **为什么不建成员表**：内部工具、十来个同事、项目数量个位数 —— 成员表的收益（按人授权）远小于它的成本（多一张表 + 每个查询都要带上成员判断 + 谁都可能忘了判）。等真的出现"某个项目不该给所有人看"的需求再说，那时加一张表也不迟。
- **为什么"跨项目混用"必须拦**：这是**静默错误** —— 指标照样算得出来、报告照样好看，但那是两个项目的数据拼出来的数；而且它不报错，只有人肉核对才会发现。放进事务里判定（而不是 handler 先查两遍），是为了消灭"校验通过之后、插入之前"被改掉的窗口。
- **为什么归属要显示邮箱**：全员可见意味着"谁能看"不再是控制手段，那么**责任**就成了唯一的约束 —— 项目列表里能直接看到创建者，比一个裸露的 `created_by=3` 有用得多。M7 之前建的项目 `created_by` 为 NULL，前端显示「创建者未知（M7 之前建立的项目）」，不留空白。
- **前端的项目上下文**：`currentProjectId` 从"写死 1"变成"真实项目列表 + 用户上次的选择（localStorage）"；记住的项目被删时**回退到第一个并明确提示**（静默换项目会让人以为在看 A、其实在看 B）；切换项目后各页面 `watch` 重新加载，否则界面还停在旧项目的数据上。
- **新部署的空项目引导**：库里一个项目都没有时，顶栏显示「建第一个项目」（仅管理员），各列表页显示引导文案 —— 否则装完系统连语料库都建不了（`project_id` 必填）。

---

## 5. 评测集建设指南（中文自建语料）

> 这是全项目最容易卡、也最不能省的环节。**先小后大**：首个领域 20–50 篇源文档 + 30–100 题即可让 M2 出可信指标，之后迭代扩量。

### 流程

1. **选题域**：选你工作或简历相关的领域（如产品文档/规章制度/某垂直行业知识库），选你**手头能拿到原文**的。
2. **收集与清洗**：统一转 markdown/txt；去页眉页脚、目录、乱码；每篇保留标题层级 → 清洗结果入 `documents`。
3. **出题**（对照原文人工写）：每篇文档先列 3–10 个候选问题，再精修。题要**必须检索该文档段落才能回答**——常识题、开放闲聊题一律剔除（否则指标失真）。
4. **标 gold 锚点**：对每题标 1–3 个稳定区间（见 D1），与出题同步做（此时你最清楚答案出处）。
5. **写参考答案**（reference_answer）：供有参考的 judge 协议使用，也为人工金标打分提供基准。
6. **打属性**：`category`（题型，如 条款理解/流程操作/对比类…）、`difficulty`（易/中/难）——M6 分层分析依赖它。
7. **自检清单**（每题过一遍）：a) 问题不含答案泄漏；b) 只凭 gold 段落能作答；c) 不依赖语料外知识；d) 参考答案与 gold 段落一致。有条件找同事抽检 20%。
8. **导入**：先做 jsonl/csv 导入脚本（M1），提供 schema 校验与重复 qid 检查。

### 导入格式示例（jsonl）

```json
{ "dataset": "contracts-qa", "qid": "c-001",
  "question": "根据合同模板，甲方逾期付款超过多少天，乙方有权解除合同？",
  "gold_anchors": [ { "doc": "d01", "span": "第五条 违约责任" } ],
  "reference_answer": "连续逾期超过 30 天…",
  "category": "条款理解", "difficulty": "易" }
```

**命名约定（M1 已落地）**：
- `qid` 带**语料域前缀**：知简 CRM 用 `zjc-001` 起（`zj`=知简、`c`=CRM）；换语料域换前缀，跨域不撞号。
- 评测集资产文件 `<域>-qa-v<N>.jsonl`（如 `zhijian-qa-v1.jsonl`）：`qa`=问答评测集类型；**版本号是承诺**——跑过评测的版本不修改，修订/扩量出下一版，保证历史 run 可对比（呼应 §5 固定 dev 集）。

### 数据规模路线

M2 起步 30–100 题 → M6 前扩到 ≥200 题 → 固定 **dev 集**（≥50 题）在迭代中永不改，专做回归对比。

---

## 6. 里程碑规划 M0–M7

> 总览：每步可演示。业余时间粗估：M0–M2 ≈ 2–3 周（先拿到第一份检索指标），M3 ≈ 1–2 周，M4 ≈ 2–3 周，M5 ≈ 1–2 周，M6 ≈ 1–2 周，M7 ≈ 1–2 周 + 标注时间另计。**估算是用来砍范围的，不是用来赶工的。**

| 里程碑 | 一句话目标 | 结束标志（可演示） |
|--------|-----------|-------------------|
| M0 | 环境与骨架 | `make up` 一键起 PG+Qdrant，API 健康检查通 |
| M1 | 语料/数据集建模 + 导入 | 导入 30 题中文评测集，页面可看可查 |
| M2 | 内置检索评测闭环 | 对评测集跑出 Recall/MRR 等真实指标 |
| M3 | 异步任务基建 | kill worker 后续跑不重算已完成 case |
| M4 | 生成侧评测 v1 | 报告能区分"召回问题/幻觉问题" |
| M5 | 实验管理与 A/B 对比 | 两份配置一键对比 + 差值表 + 显著性 |
| M6 | Bad Case 标注 + 金标校准 | 完整迭代闭环 demo（发现→修→复测） |
| M7 | 多用户、打磨、部署 | 同事登录即可用、看报告、标 case |

---

### M0 环境与骨架

**前置**：§9 待拍板项 3/4/5 至少给方向（不影响骨架，影响 M2 配置）。

**任务清单**（✅ 2026-09-09 全部完成，tag `v0.1.0-m0`）
- [x] git 仓库规范（main 分支、提交信息规范、.gitignore 含 `.env`）
- [x] 目录骨架落地（§7 结构）+ README 开头
- [x] `docker-compose.yaml`：postgres + qdrant（+ 可选 redis），健康检查与卷
- [x] Go API 骨架：Gin + 配置加载 + 日志 + `/healthz`（含 PG/Qdrant 连通检查）
- [x] golang-migrate 接入，首个空 migration + `make migrate`
- [x] Python worker 骨架：pyproject + 依赖分组(dev/runtime) + 日志(structlog) + 连 PG/Qdrant 的最小脚本
- [x] Makefile：`up-deps/api/worker/web/migrate/test`
- [x] 前端脚手架：Vue3+TS+Vite+Naive UI+路由+布局空壳（登录页占位）

**验收标准**：新机器 clone 后按 README 三步能起依赖并访问 API 健康页；`make test` 在各端有占位测试跑通。

**测试与提交**：Go handler 冒烟测试、Python 连通性测试（pytest，连不上则 skip）；每步一个 commit。

---

### M1 语料与数据集建模（数据层 MVP）

**任务清单**（✅ 2026-09-09 完成）
- [x] PG 迁移：§3 表中 `projects/users/corpora/documents/datasets/cases`（不含后续实验表）
- [x] Go：三组 REST API —— 语料库 CRUD、文档上传（文本/文件）、数据集与 case 管理（导入 jsonl、列表、详情、删除）
- [x] case 导入校验：qid 唯一、gold_anchors 引用存在的 doc、必填项
- [x] 前端：语料库 + 数据集 + case 列表三页（表格、导入对话框、表单），遵循 D9 设计基线
- [x] 中文样例语料 & 30 题评测集落地（见 §5）→ 入库
- [x] 单元/集成测试覆盖导入校验主路径

**验收标准**：网页上传中文文档、导入 30 题 jsonl、查得到、改得到、错误导入有明确报错。

> 落地资产：语料 21 篇全量入库（corpus 4）；评测集 `datasets/qa/zhijian-qa-v1.jsonl`（zjc-001~030，dataset 3，`imported=30, errors=0`）。
> 导入重复检测语义已定：**仅"有效且入库"的行占用 qid**（坏行不占坑，方案 A）。

---

### M2 内置检索评测闭环（第一份真实指标）

> 全项目第一个"出数"的里程碑。不碰 LLM，纯检索 + 确定性指标，先建立可信基线。

**任务清单**（✅ 2026-09-10 完成；M2-7 拆为 Go 报告 API + 前端报告页两张卡）
- [x] Qdrant collection 管理：按 (语料, chunking_hash) 命名/隔离；payload 存 doc_id/span 映射/chunk 文本
- [x] chunker（内置实现，含"按标题层级/段落/固定大小"等策略）+ chunking_hash 记录（D7）
- [x] embedding 供应商接入（OpenAI 兼容封装，先接一个，见 §9）
- [x] 锚点映射器：gold anchor 区间 → 当前 chunk 集合 `G`（D1），含映射失败校验
- [x] 检索器：top-k 查询 + 可选手工 rerank（M2 先不做 reranker 也 OK）
- [x] **最简评测 runner**（先同步跑通，M3 再异步化）：对每题取 `Rk`，算 Recall@k/Precision@k/MRR@k/Hit@k（口径 §D10）
- [x] runs/jobs/job_items/case_results 表落地 + 结果落库（为 M3 打底）
- [x] 报告 API + 前端报告页雏形：指标卡片 + 每题明细表
- [x] Python 侧指标计算单元测试（构造小样例手算核对）+ chunker/锚点映射测试

**验收标准**：用评测集跑出可信指标；**换一个明显更差的检索配置（如 top_k=1）指标随之下降** —— 证明系统"能测出好坏"，这一步是简历 demo 的起点。

> **M2 基线数据（写进 README 与简历）**：
> - 语料 21 篇 → headings 切分（chunk_size=500/overlap=50/min_chars=80）→ **80 chunks**，指纹 `chunking_hash=5f45e034…`，collection `corpus4_5f45e034`
> - 评测集 30 题，gold 锚点映射覆盖率 **100%**（平均 1.37 个 gold chunk/题）
> - 基线与配置：`bge-m3`(1024 维) + top_k=5 → **Recall@5 91.4% / Precision@5 22.0% / MRR@5 83.4% / Hit@5 100%**
> - 落库样例：run #3（`config_hash=b6598ed9…`, `git_sha=1dbf358`, 30 条 case_results）
> - 已能区分三类问题：召回不全（zjc-026 recall 0.25）、排序靠后（zjc-027 命中但排第 5）、跨文档干扰（zjc-014）
> - 待验证的对比实验（M5）：top_k=1/20、fixed 切分、开 reranker —— 用于证明"指标能测出好坏"

---

### M3 异步任务基建（可靠性核心）

> 面试含金量集中点：持久化队列、checkpoint 续跑、心跳、重试、死信。

**任务清单**（✅ 2026-09-11 完成，tag `v0.3.0-m3`）
- [x] job 领取协议：worker 轮询 `FOR UPDATE SKIP LOCKED`；job 状态机 `pending→running→succeeded/failed`，job_items 状态机细化
- [x] **checkpoint 提交**：逐条 case 独立事务写 `case_results` + 推进 `job_items`（D2）
- [x] 心跳与超时接管：`heartbeat_at` 刷新；worker 端恢复逻辑=扫描 running 且心跳过期的 job_items 重新入队
- [x] 重试与退避：瞬时错误按类型重试（`retry_count` 上限）→ 超过进 failed 并记录原因；不吞错
- [x] 进度：`jobs.progress` 汇总 + `GET /runs/:id/progress` 轮询 API + 前端进度条（活跃任务 3s 自动刷新）；**WebSocket/SSE 见 D13 backlog**（原清单里的"WebSocket 推送"未做，故在此显式改写，不留假勾）
- [x] 把 M2 的同步 runner 迁入 worker（`app.cli.run_retrieval_eval` 保留为离线对照工具，见 `docs/reliability.md` §6）；批量大小 `--batch-size` 可配，并发粒度=job 级（D12）
- [x] 故障演练文档 + 脚本：跑一半 `kill -9` worker → 重启 → 续跑只补剩余 case（`scripts/fault_drill.sh` + `docs/fault-drills.md`）
- [x] 集成测试：多 worker 并发领取不重复执行（幂等断言）、中断续跑不重算、死信路径
      （Go `queue_live_test.go` 7 条 + worker `test_queue_runner.py`/`test_reclaim_live.py`/`test_concurrency_live.py`）

**验收标准**：50+ 题 run 进行中杀 worker，重启后从断点续跑完成，结果与全量跑一致；前端能看到实时进度。✅ **已达成**

**M3 完成小结（2026-09-11）**

- **规模回归**：v2 60 题（dataset 4，`chunking_hash=5f45e034` 未变，与 v1 同向量空间）。参照 run #100 全量跑完 → 演练 run #101 中途 `kill -9`（崩溃时已完成 4 条）→ 接管后续跑 `processed=56`（恰好等于剩余）→ `compare_runs` **逐题一致（退出码 0）**，指标逐位相同：`recall@5 0.948611 / precision@5 0.23 / mrr@5 0.883889 / hit@5 1.0`。
- **并发粒度**：job 级（D12）。两 worker 各持一 job 并行，采样 32 次中 15 次同时 `running`；同一 job 二次领取必失败（Go/worker 各一组 live 断言）。
- **进度可视化**：`job.progress`（checkpoint 事务内 `count(*)` 重算）+ `/runs/:id/progress`（含 `stale` 疑似掉线）+ 前端进度条与 3s 轮询（D13）。
- **可复现性证据**：`config_hash=b6598ed9` 下 run #3/#22/#32/#99 四次 run（`git_sha` 分别为 `1dbf358/484f26d/d54c4e6/9e39459`）`recall@k` 逐位一致；同时测出**分数噪声底**（同 chunk 跨 run 中位 3e-4、最大 2.1e-3）→ 写入 `compare_runs` 输出，作为 M5 判定门槛。
- **指标口径发现（转 M5 输入）**：k=5 时 60 题中 55 题满分、0 题未命中（饱和）；k=1 时 recall 0.6736 / mrr 0.7833（13 题未命中）。**配置对比应看 k=1/3 与 MRR**；离线用 top-5 列表重算 k 的结果与真跑 `top_k=1` 完全吻合，可省真跑。
- **数据质量项（转 v3）**：v1 的 zjc-017/018/024/026 锚点 span 与文档标题同词 → 命中整篇文档、gold 膨胀、**Recall 被系统性压低**（v1 全部召回损失的算术来源即这 4 题）。"召回不全"的正确示例改为 zjc-031（跨文档漏召 D02）。
- **偏差记录**：① 原计划的"WebSocket 推送"改为轮询（D13）；② `--once` 会领走任意 pending 任务，调试纪律改为一律 `--job-id`（已写入演练手册）；③ 演练脚本改为默认保留 run（它是一致性验收的右操作数）。
- **文档**：`docs/reliability.md`（设计详解 + 三条面试话术）、`docs/fault-drills.md`（演练手册 + 实测记录 + 已知边界）、`README.md` 可靠性章节。

---

### M4 生成侧评测 v1（Judge 与幻觉归因）

**任务清单**
- [x] （M4-1）生成链路（builtin）：调生成 LLM 产出 answer，记录 prompt id/模型/温度到配置快照（D14；基线 run #141）
- [x] （M4-2）judge 协议 v1（自研 prompt + 严格 JSON 输出 + 解析校验）：
  - [x] claim 分解器（D4）
  - [x] claim × 检索上下文 entailment 判定（supported/unsupported/irrelevant）
  - [x] relevance/helpfulness rubric 打分（中文 rubric，**v2**：加 `claims_summary` 占位符 + "有 unsupported 则 helpfulness ≤ 2" 硬规则）
- [x] （M4-3）**归因规则落地**（D16）：`no_gold / anchor_incomplete / retrieval_miss / retrieval_partial / retrieval_low_rank / hallucination / off_topic / generation_quality / no_claims`（原计划的 `context_missing` 因无可计算谓词而缺席，理由见 D16）
- [x] judge 缓存（D5/D8）；token 用量落库
- [x] （M4-4）报告扩展：生成侧/判定侧指标卡片 + 每题主因标签与断言计数 + 断言逐条高亮（幻觉红行）
- [x] （M4-4.1）检索上下文正文：`GET /runs/:id/cases/:case_id/context` 按需从向量库取（D17），抽屉里"上下文 vs 答案"并排
- [x] （M4-5）前端：case 详情抽屉（合并进 M4-4/M4-4.1 交付，不再单列）
- [x] 测试：judge 解析异常重试、claim 分解与判定的小样例、归因规则单测、缓存命中不重调用

**验收标准**：跑 30 题能看到"幻觉率 X%、召回问题 N 条、幻觉 M 条"，且能点开看**具体是哪句答案在编**。（先接受 judge 可能不完美，M6 校准。）✅ **已达成**

**M4-3 完成小结（2026-09-14）**

- **规则内核**：`worker/app/eval/attribution.py`（纯函数、零 I/O）——同一套规则既服务队列落库，也服务 CLI 对历史 run 重算，**规则升级不需要重跑 LLM**（run #155 一次判定 101k judge tokens，重算 0 token）。阈值与规则版本进 `runs.metrics.attribution`（D15）。
- **回放验收（真实数据，数字与卡片预测逐字吻合）**：run #112（k=1，只检索）→ `retrieval_miss=13 + retrieval_partial=12`；run #100（k=5）→ `retrieval_partial=5 + retrieval_low_rank=1`（zjc-027 排第 5）；run #155（k=5，生成+判定）→ 同上 6 条，`scope=retrieval+judge`，判定类标签 0。
- **不动指纹**：`--apply` 前后 `config_hash` 逐字一致（`712f6b17` / `4e767020` / `449ca546`）；重算幂等（再跑一次"差异 0"）。
- **端到端可见**：`GET /api/v1/runs/112/report` 直接返回 `flag_counts={'retrieval_miss':13,'retrieval_partial':12}`，**Go 侧零改动**（M2 时预埋的 `ListRunFlagCounts` 如期复用）。注意 `flag_counts` 只在 **report** 接口，不在 `/runs/:id` 详情接口。
- **k=1 → k=5 的归因叙事（转 M5 的验收靶子）**：run #112 的 13 道 `retrieval_miss`，在 run #100 里 12 道变干净、1 道降级为 `retrieval_low_rank`，**0 道转成幻觉/质量问题** —— 即"提高 k 修好了 13 道检索漏召回，且全部修在检索环节"。
- **工具边界（卡片写错、此处更正）**：`make compare` 是**一致性校验**工具（故障演练用），配置指纹不同时按设计拒绝并提示"请用 M5 的报告工具"；跨配置的标签差异属于 **M5 A/B 报告**，不是它该干的事。
- **交付流程教训（已生效）**：修正已交付的文件必须**重发整份**，不能用"替换片段"。本轮我把两处修正给成片段，被贴成新增 → 同名测试函数重复定义、Python 取**最后一个** → 断言被旧版静默覆盖，`make worker-test` 报错才暴露。测试能抓到它，正是"每条规矩都要有用例守着"的价值。
- **未做/转出**：① run #156（k=1 + 生成 + 判定）可量化"幻觉的根因是检索"，约 120k tokens，列为可选；② 用 `cases.reference_answer`（已在库，无需迁移）做"要点覆盖"检测 —— 可靠判定要第三次 LLM 调用（judge 成本 +50%），关键词覆盖又会产生假警报，等 M6 人工金标显示 judge 系统性漏判"漏答要点"时再做；③ 失败条目（`job_items` 终态 failed，无 `case_results` 行）在报告里不可见，已在报告页顶部用一条提示兜住（读 `/runs/:id/progress` 的 `failed`）。

**M4-4 / M4-4.1 完成小结（2026-09-14）**

- **数据早就够了，缺的是"露出来"**：`/runs/:id/report` 从 M4-1/M4-2 起就返回每题 `answer/generation/judge` 与 34 个 run 级指标，但前端只画了检索侧 6 个数字。M4-4 因此是**纯展示层**工程：报告卡新增「生成与判定」12 项（三率、均值、token、缓存命中、断言总数），底部一行"归因口径：规则 v1 · k=5 · 排序阈值 >3 · 达标线 ≤3 · 覆盖 retrieval+judge"（D15 的凭据直接可读）。
- **三率自洽在界面上硬校验**：`claim_support_rate + hallucination_rate + irrelevant_rate` 必须等于 1（容差 1e-5，覆盖三项各自四舍五入到 6 位的误差）；不成立时报告页顶部自己冒红条"先别拿这份报告下结论"。口径错了要吵出来，不能靠人眼。
- **主因在前**：每题表新增「主因」列 = `flags[0]`（D16 的顺序即优先级），配中文标签与配色（幻觉/不可评测红、检索类黄、质量类蓝）；「断言」列用紧凑计数 `3/0/0`（支持/无据/无关），**出现无据/无关即标红加粗**。抽屉里断言逐条列出、无据行整行红底，`evidence` 与判定理由并排 —— 这就是"具体是哪句在编"。
- **上下文正文按需取（D17）**：新端点 `GET /runs/:id/cases/:case_id/context` 用 `corpus_id + 切分指纹` 推集合名，向 Qdrant `POST /collections/{c}/points` 取 payload 正文；**历史 run 不必重跑**。实测 run #155 / zjc-027 取回 5 条正文且第 5 条正是 gold（`D01 / 签名与重试`，里面写着"失败指数退避重试 3 次（5s / 30s / 5min）"）—— 与 M4-3 给它打的 `retrieval_low_rank` 完全对上：**答案就在那儿，只是排最后**。这一屏就是"排序问题不是幻觉问题"最好的教具。
- **降级设计**：向量库不可用 / 集合缺失 / 快照缺 `chunking` 段 → 200 + `error` + `chunks[].found=false`，前端退成"只列 point_id/doc_id/score"的骨架并提示原因；旧后端（没有这个端点）时抽屉也不空白。
- **UI 踩坑（值得记）**：内容容器 `max-width: 1200px`（可用约 1110px），我一次加了 12 列、固定宽度合计 1330+px 又**没设 `scroll-x`** → naive-ui 直接把超出部分裁掉，每行右侧内容"看不见"。修法两条一起：① 收紧列宽 + 删掉与「主因」重复的「全部标签」、把「类别」挪进抽屉，固定列压到 886px 放得下；② 两张表都设 `scroll-x` 兜底，`qid`/`操作` 固定左右。另修一个 tooltip 坑：单元格里先截断会让悬浮 tooltip 也只剩半句，改成列级 `ellipsis` + 不截断渲染。
- **验证**：worker **292 passed**、Go 4 包 ok、web **50 passed**（`report.ts` 纯函数 30 条）；`pytest`/`go vet`/`vue-tsc` 全绿。真数据端到端核对过接口字段与 TS 类型一致（`score` 为 float、`found` 为 bool）。
- **决策**：新增 **D17**（正文按需取、不落库），与 D14（快照唯一权威）、D15（口径不重复落库）同一取向：**宁可按需取一次，也不制造第二个真相源**。

---

### M5 实验管理与 A/B 对比报告

**任务清单**
- [x] （M5-2）`pipeline_profiles` 配置模板管理（CRUD）+ 校验（配置形状/范围/判定依赖生成，与提交侧**同一套**规则）
- [x] 配置快照与指纹：run 建立时固化 `config_snapshot` + `config_hash` + `git_sha`（D7）；M5-2 追加**提交前预览指纹**
- [x] run 列表/详情：同数据集多次 run 的历史与状态（M2/M3 已完成，M5-1 补上"选两次 run 对比"入口）
- [x] （M5-1）**A/B 对比**：per-question 差值 + 显著性 + 分层（category/difficulty/flag）+ 翻转题清单（D6/D18；Wilcoxon **自研**，不引 scipy）
- [x] 报告导出：差值表 CSV / Markdown / JSON（前端用同一份结果导出，服务端不写第二遍格式化）
- [x] 前端：对比页（并排指标 + 修好/变坏两栏 + 分层表 + "怎么读这份报告"）、配置模板表单（分组可折叠 + 指纹预览）
- [x] 测试：`config_hash` 稳定性（预览 == 真提交，且能复现历史 run）、A/B 计算正确性（手算 Wilcoxon 样例 + 负控零差异）、导出格式（CSV 转义/Markdown 表格）

**验收标准**：用同一评测集跑 2–3 组不同配置，一键产出"谁在哪些题上赢"的对比报告；hash 复现定位历史 run 可演示。✅ **已达成**

**M5-1 完成小结（2026-09-17）**

- **三条证据并排，而不是一行 p 值**（D18）：`GET /api/v1/compare?left=112&right=100` 同时给①逐题差值（paired）②Wilcoxon p 值与噪声底判定③**翻转题清单**。理由是检索指标离散且大量并列（recall 常只有 0/0.5/1），正态近似在大样本同分下不可靠 —— 结论以"噪声底 + 翻转题"为准，p 值只作旁证（这条也写进了对比页的说明文案）。
- **真实验收（不花 token）**：#112（k=1）vs #100（k=5）→ `recall 0.6736 → 0.9486`（**+27.5pp**）、MRR +10.1pp、`p=2e-06`、`below_noise=false`；**修好 19 题 / 变坏 0 题 / 标签变化 1 题**（那 1 题正是 zjc-027：`retrieval_miss → retrieval_low_rank`）；`by_flag` 里 `retrieval_miss 13→0`、`retrieval_partial 12→5` —— 与 M4-3 小结逐字吻合。
- **负控**：#100 vs #101（同 `config_hash` 的演练对）→ 全部差值 0、`p=1`、`fixed=broke=changed=0`、`by_flag` 两侧计数逐项相同。工具不会自己造噪声。
- **负控抓到的真 bug**：第一次跑出 `fixed=6` —— 因为 run #101 从没做过归因（`--apply` 只跑过 112/100/155），它的 `flags` 是空的，于是每道有标签的题都被算成"被修好"。修法是加护栏 `attribution_missing`：用 `runs.metrics.attribution` 是否存在判断"标签是否可信"（D15 的凭据复用），**只在一侧缺失时**报警并给出可执行下一步；随后给 #101/#111 补做归因，负控才成为真正的负控。
- **第二个真 bug（测试抓的）**：`hallucination_rate` 是**越低越好**，但通用计数把"幻觉率下降"记成 `Worsened`、前端还会涂成红色。修法是给指标加方向 `HigherIsBetter`（幻觉率/无关率为 false），改善/恶化计数与配色都按方向走，前端还标出"越低越好"。
- **生成侧指标只在"两侧都有判定"的题上比**：`MetricSpec` 把"某题在该指标上是否可比"交给指标自己回答，缺判定的题不进配对样本（否则"没判定"会被读成"零幻觉"）；`cases` 字段把实际样本量显示出来，`judged_cases=0` 时给 `generation_note` 说明"为什么没得比"。
- **交付边界（卡片写错处更正）**：`make compare` 是**一致性校验**工具（故障演练用），跨配置按设计拒绝并提示"请用 M5 报告工具"；跨配置对比是 M5-1 的 `/api/v1/compare`。

**M5-2 完成小结（2026-09-17）**

- **模板只装"提交请求的旋钮"**：`chunking / retrieval.top_k / generation / judge`，语料库与评测集在提交时才选 —— 模板到提交请求是**无损一一映射**，不会出现"存了却不生效"的幽灵字段；未启用的阶段写 `null`（与 D14"段不存在 = 不启用"同义），所以模板不会凭空打开一个实验阶段。
- **指纹不落库，改为"提交前现算"**：`config_hash` 含 `corpus_id/dataset_id`（D7），模板没有这两项，存一个"模板自己的 hash"只会误导。新增 `POST /api/v1/pipeline-preview` 返回 `{snapshot, config_hash, chunking_hash, collection}`。**实测它与历史 run 的真实指纹逐字相同**：k=5 → `4e767020…`（= run #100）、k=1 → `712f6b17…`（= run #112）。
- **预览与提交共用同一套解析规则**（`submit_config.go`）：`resolveChunkingRequest / resolveGenerationRequest / resolveJudgeRequest / resolveTopK` 被两边调用，避免"模板说有 500 字切分、真提交按 300 跑"这类最难查的偏差；`runs.go` 的 `resolveGeneration/resolveJudge` 改成薄委托。
- **校验与错误语义**：模板侧拒绝"启用判定但未启用生成"（与提交侧同一句话术）、切分非法、`top_k > 50`；同项目重名 409、项目不存在 404。
- **路由踩坑**：预览放在顶层 `/pipeline-preview` 而不是 `/pipeline-profiles/preview` —— gin(httprouter) **同一层不允许静态段与 `:id` 通配段共存，会直接 panic**（与 M5-1 的 `/compare` 同一原因）。
- **工具链**：新增 `make api-restart / api-logs / api-stop`。两个坑写进了 Makefile 注释：① `lsof -ti :8080` 会把**连到该端口的客户端**也列出来（照杀会误伤无关进程），必须加 `-sTCP:LISTEN`；② 后台启动要 `</dev/null >日志 2>&1`，否则后台进程继承调用方的管道，会出现"make 结束了但调用方读不到 EOF"。
- **未做/转出**：run 提交表单里"从模板一键填入配置"（后端与模板页都就绪，只差提交页 M5-3）；生成侧指标的 A/B 真实正例需要第二个"有判定"的 run（当前库里只有 #155 有判定，正例路径由单测覆盖）。


---

### M6 Bad Case 标注与金标校准闭环

**任务清单**
- [x] 标注工作台：从报告/flag 过滤进入 → 打标签 + 评论 + 状态流转 `open→fixed→verified`（annotations 表；M6-1 后端 + M6-2 前端）
- [x] "bad case → 归因建议"：自动给出检索/幻觉/生成建议（基于 flag 与 judge 判定），人工确认或纠正（`/annotation-suggestion`：`flags[0]` 即主因 D16；无判定时拒绝给"幻觉"结论）
- [x] 人工金标集流程：从评测集选 30–50 题，双人/单人 + 复核打分入 `human_gold_scores`（M6-3a 后端 + M6-3b 打分页；`reviewed` 复核标记 + 复核比例进报告）
- [x] **judge 校准报告**：自动评测 vs 人工金标的一致性（κ / 一致率 / 混淆矩阵 / MAE / 均值偏差 / 人工间一致性），并自曝"样本<20 / 覆盖率<50% / 单人未复核"三类不可信条件
- [x] 迭代闭环支撑：改 profile 配置 → 重跑 → 对比"上一版 vs 这一版"中已标 fixed 的 case 是否变好（M6-4：`GET /closure` + 闭环页，闭环页可直接"改配置重跑"并一键销单）
- [x] 前端：标注面板（快捷键友好）、金标打分页、校准报告视图、闭环页
- [x] 测试：标注状态机、校准指标计算（κ 手算样例/混淆矩阵/边界）、闭环对比查询（五种结局/状态矩阵/护栏）

**验收标准**：完整 demo —— 发现一批 bad case → 打标归类 → 改配置重跑 → 报告显示该批指标回升且对应 case 状态推进到 verified。✅ **已达成（代码与真机核对均通过；人工标注/金标数据由使用者在界面里产出，见下方小结第 2 条）**

**M6 完成小结（2026-09-17）**

**1. 交付物（六个子批次，全部已提交）**

| 批次 | 内容 | commit |
|------|------|--------|
| M6-1 | `annotations` 表 + 状态机 + 统计 + 归因建议（6 个端点） | `81565a4` |
| M6-2 | 标注工作台（快捷键 1-5/n/b、归因建议采纳、状态流转、评论） | `81565a4` |
| M6-3a | `human_gold_scores` 表 + κ/MAE/混淆矩阵/人工间一致性 + 5 个端点 | `81565a4` |
| M6-3b | 金标打分页（1/2/3 判词、judge 断言并排、按需取上下文正文、星级后补、复核标记） | `e9d9574` |
| M6-4 | 标注闭环报告 + 闭环页（五种结局、销单清单、**改配置重跑**对话框） | `5972488` / `e9d9574` |
| 收口 | 本轮补齐：闭环状态矩阵、`reviewed` 复核信号、重跑入口、文档 D20/D21 | 本 commit |

**2. 真机核对（数字都是跑出来的）**

- **闭环正向**：run #112（k=1）上把 3 道 `retrieval_miss` 标成 fixed → `GET /closure?baseline=112&candidate=100` 报 `improved=3 / eligible_for_verify=3`，证据 `recall 0% → 100%`、`MRR@k 0% → 50%`；`PATCH /annotations/:id {"status":"verified"}` 销单成功，再想改回 fixed 被 409 拦住（状态机生效）。
- **状态矩阵**（本轮补齐）：同一批题分别标成 `fixed / verified / wontfix`，闭环给出三句不同的话——「可销单」/「这条已经验证过了, 不用重复销单」/「当时判为不修(wontfix), 但这次确实变好了: 可以改成 verified」。含糊的"还不能销单"等于没给结论。
- **护栏**：#100 vs #111（同为 `4e767020`）→ `same_config=true`，报告第一句就是"这是复现而不是实验"；`baseline=100 → candidate=112` 反向对比时同一道题报 `worsened`；候选 run 未做归因时整体降级为不可判断并给出 `make attribution RUN=<id> APPLY=1`。
- **校准与复核**：打分页 ✔ 复核开关 → `PATCH /human-gold/:id {"reviewed":true}` → `GET /judge-calibration` 的 `gold_reviewed` 从 0 变 1，并新增提示「有 1/1 道金标经过复核」；零复核且样本 ≥20 题时改为提示「建议至少抽 20% 双人复核, 否则 κ 只是一个人 vs judge 的口径」。
- **改配置重跑**（M6 验收的中间一环）：从 run #112 的 `config_snapshot` 预填、只把 `top_k` 1→3，提交得 **run #156**（60 题、`config_hash=022705e7`、快照里 `retrieval.top_k=3` 且无 `generation`/`judge` 段、数据来源沿用 dataset 4 / corpus 4），闭环侧 `same_config=false`。这一步以前只能手写 curl，参数记错一个就变成另一场实验。
- **测试**：worker `292 passed / 20 skipped`；Go 四包 ok；web **171 passed**（M6 净增 68 条：标注 16 + 校准 25 + 闭环 26 + 重跑 17，含 κ 教科书样例 0.6、混淆矩阵逐格、`evidenceHighlights` 优先级、`formFromRunSnapshot` 的错类型字段）。

**3. 两条踩坑记录（都已用测试钉住）**

- **"没传" ≠ "清空"**：第一次真机冒烟就发现 `POST /human-gold` 只带判词时，`COALESCE` 之前的整行覆盖把上次的分数与备注冲成 NULL/空串——**校准样本凭空少了几对而接口调用方看不出异常**。改成 `COALESCE(EXCLUDED.x, 原值)` 合并语义 + 一条守卫测试；`PATCH` 侧再补"传 0 = 撤回分数"（1–5 之外的值正好空出来当清空信号）。
- **"变好"的定义只能有一份**：闭环要把题推进到 `verified`，如果它自己再写一遍判定，迟早出现「对比页说修好了、闭环页说没修好」。于是把 A/B 里的标签转移抽成 `eval.FlagTransition`，两处共用（`/compare` 的 Fixed/Broke/Changed 与闭环的 improved/worsened/changed 同源）。

**4. 未做/转出**

- **人工数据必须由人产出**：`annotations` 与 `human_gold_scores` 目前都是 0 行（冒烟数据已清理，我不用"机器代替人工"的方式造校准结论）。校准报告现在显示空态「还没有人工金标: 先去打分页挑 30–50 题判一遍」——这是设计上正确的空态，不是缺陷。
- **run #156 已入队**（`top_k=3`、只跑检索、无 LLM 生成/判定费用，`make worker` 即可跑完）：跑完可在闭环页直接验证"把 k 从 1 提到 3 之后那 3 道漏召回是否修好"，这是 M6 验收最省事的一条真实数据。
- **生成侧闭环仍缺正向案例**：现有 A/B 只有 `k=1 vs k=5`（纯检索）。要演示「幻觉的根因是检索」，仍需一次 `k=1 + 生成 + 判定` 的 run 与 #155 对比（约 120k tokens，可选）。
- **D20/D21 之外的取舍**：标注不挂 dataset（D20）意味着换 run 要重标；本轮接受该成本，若将来标注量上来，可考虑"从旧 run 复制标注"的辅助功能（明确标为"建议值"，仍需人确认）。

---

### M7 多用户、打磨与部署交付（同事可用）

**任务清单**
- [x] 登录/注册 + JWT + RBAC（admin/editor/viewer，权限落到 API 与前端路由）—— M7-1 完成，见下方小结
- [x] project 隔离完善：成员管理、资源归属、跨 project 只读隔离校验 —— **M7-2 完成**（决策见 §4 D24：不建成员表，`created_by` + 全员可见）
- [~] UI 打磨：设计基线统一（空/载/错状态、表格、图表配色）、报告导出按钮、标注页体验
      —— 已完成：①顶栏导航截断修复(导航独占一行, 实测 900px 以上 11 项全完整) ②只读账号写按钮置灰(含全局只读横幅)
      ③统一三态组件 AsyncState(语料库/数据集已接入) ④报告导出(CSV/Markdown)
      —— 未完成：其余列表页接入 AsyncState、图表配色统一、标注页快捷键提示的打磨（见 M7-3 小结的"未做"）
- [x] Docker Compose 一键部署（api+worker+web+pg+qdrant+可选 redis），`.env.example` 完整注释 —— **M7-4 完成**
- [x] 部署文档：内网机器三步启动、备份（PG dump + Qdrant snapshot）、升级流程 —— **M7-4 完成**（`docs/deploy.md` + `scripts/backup.sh`）
- [ ] README 成稿：项目动机、架构图、指标口径、**参考调研章节（DeepEval/RAGAS 等对比与借鉴点，注明核心自研）**、demo 脚本
- [ ] 全量测试回归 + 演示录制脚本（一步步讲：上传→评测→报告→对比→标注）
- [ ] 找同事试用一轮，收集 3–5 个真实反馈并修掉高优项

**验收标准**：同事在部署环境注册登录，能独立完成"建项目→导入数据→跑评测→看报告→标 Bad Case"全流程；README 能支撑你面试讲 20 分钟。

**M7-1 完成小结（2026-09-17）：登录 + JWT + RBAC**

**交付物**：迁移 `000009_user_auth`（`users` 补 `disabled`/`last_login_at` + email 小写约束）；`internal/auth`（bcrypt 口令、HS256 JWT 签发/校验、5×3 权限矩阵、gin 中间件）；`/auth/*` 四个端点 + `/users/*` 四个管理端点；路由按权限分成五个组；前端登录页（含"首次创建管理员"）、会话 composable、全局路由守卫、权限不足页、用户管理页、按角色裁剪的菜单与当前账号下拉。

**真机核对（都是跑出来的）**
- 空库 → `GET /auth/status` 报 `bootstrap_needed=true` → 首次注册直接成为 admin 并拿到 token；再注册返回 409「注册已关闭」。
- 无 token 访问 `/runs` → 401；伪造/`alg=none` token → 401；带 token → 200。注册响应里**不含**口令哈希。
- viewer：`GET /runs` 200、`GET /closure` 200；`POST /runs`、`POST /annotations`、`DELETE /datasets/4`、`GET /users` 全部 **403**。
- 自我保护：管理员改自己的角色 → 409「不能修改自己的角色/停用或删除自己」；删除最后一个可用管理员 → 409。
- **停用立即生效**：停用 viewer 后，它手里那个还没过期的 token 下一次请求就变 401「账号已停用」。
- 前端链路：dev server(5173) 代理 `/api` 正常，`/login` 200；把库清回 0 账号后 UI 回到"首次创建管理员"。

**测试**：Go 新增 **30 条**（口令哈希与 72 字节边界/加盐、JWT 篡改·`alg=none`·错密钥·错 iss/aud·过期·缺载荷、权限矩阵逐格、首用户 bootstrap、最后管理员保护、以及 `TestEveryAPIRouteRequiresToken` —— 它把路由表里每个非公开端点打一遍，要求无 token 全是 401，**新端点忘挂权限就直接红**）；web 新增 **21 条**（权限矩阵、守卫三态、会话解析脏数据、菜单裁剪、开放重定向防护）。全量：worker 292 / Go 五包 / web 196。

**踩坑记录**
- 路由大重排时漏掉了两条已存在的路由（`POST /corpora`、`DELETE /pipeline-profiles/:id`），是**已有测试**抓出来的 —— 这正是"每个端点都要有用例守着"的价值，也促成了上面那条路由表级守卫。
- `main.go` 对 `JWT_SECRET` 采取 **fail-fast**（缺密钥直接拒绝启动），同时 `Makefile` 的 `api`/`api-restart` 改成先 source `.env`：否则"密钥只在某个终端 export 过"会让本地启动时好时坏。

**未做/转出**
- **按钮级置灰**：只读账号现在点写操作会收到 403 提示（而不是按钮变灰）—— 归到 M7-3 UI 打磨。
- refresh token / 服务端撤销 / 登录限流 / OIDC：按 D22 不做；停用账号已覆盖"立刻断开"的主要诉求。
- project 级成员隔离（谁能看哪个项目）是 **M7-2**，本轮只到"角色级"权限。

> ⚠️ 本地 `.env` 里由本轮写入了一个开发用 `JWT_SECRET`（该文件已被 gitignore）。**部署到别处必须用 `make jwt-secret` 重新生成**；轮换它会让所有已签发的 token 立即失效。

**M7-2 完成小结（2026-09-17）：项目归属与跨项目隔离**

**交付物**：`store/project.go`（`GetProject` + 创建记 owner + 列表/详情 LEFT JOIN 出 `owner_email`）、`corpus.go`/`dataset.go`（创建记 owner）、`queue.go`（**事务内**的项目一致性校验 + `ErrProjectMismatch`）、`http/projects.go`（新增 `GET /projects/:id`）、三个创建接口传当前登录用户；前端 `api/projects.ts`、`utils/projectContext.ts`（纯逻辑 + 21 条测试）、`composables/useProject.ts`（真实项目 + 记住选择 + 回退提示）、顶栏项目切换器与「建第一个项目」、四个列表页按项目过滤并在切换后重载。**无需迁移** —— `projects/corpora/datasets` 的 `created_by` 列 M1 就有了，缺的只是"往里写"和"往里拦"。

**真机核对**：建项目返回 `created_by` + `owner_email`；`GET /projects/:id` 200 / 不存在 404 / 非法 id 400；viewer 能读项目列表但拿不到 `/users`；**数据集 A + 语料 B → 400「不属于同一个项目…一次实验只能在一个项目内进行」**；显式 `project_id` 与数据来源不一致同样 400；同项目但空数据集则走到「评测集没有用例」（说明前一道校验通过了）；被拒绝的提交**零残骸**（事务回滚）。

**测试**：Go 新增 **14 条**（`isolation_test.go` 5 条 + `isolation_live_test.go` 3 条真库 + 若干 stub 断言），web 新增 **21 条**；全量 worker 292 / Go 五包 / web 209 全绿。

**踩坑记录（三条，都已用测试钉住）**
1. **`ListDatasets` 的 SELECT/Scan 列数不匹配**：给查询加了 `d.created_by` 却忘了给 `Scan` 加参数 —— **一调列表接口就报错**，而 `go test`/`vue-tsc` 全绿也照样漏（HTTP 测试用 stub，SQL 没人真跑）。只有 `RUN_LIVE=1` 的真库用例能发现；修完后把这条断言写进了 `TestLiveOwnershipRecorded`。
2. **`CreateProject` 的创建响应缺 owner**：只 `RETURNING` 原始列就不带 JOIN 出来的 `owner_email`，同一个字段"列表里有、创建后没有"会让人以为写入失败。改成"插入拿 id → 用带 JOIN 的读法回读整行"。
3. **队列 live 测试会误伤真实任务**（这组测试操作的是**全局队列**）：`TestQueueClaimWhenEmptyLive` 会把队列里的 pending 任务领走并标成 `failed` —— 你从页面提交的评测如果正好在队列里，就会被测试"跑失败"；并发领取测试则会把真实任务抢成 `running` 后不管（页面上永远停在"运行中"）。我实际撞上了：run #156 被抢成 running、还有一条测试因为队列非空而静默 `SKIP`（等于悄悄少跑一条）。现在加了 `requireExclusiveQueue` 守卫：**队列里有别人的任务就跳过，并打印"先 make worker 跑完再回来"** —— 既不误伤真实任务，也不静默少跑。

**未做/转出**
- 项目改名/删除、成员/归属转移：M7-2 只做"归属留痕 + 隔离校验"；资源都挂在项目下，删除项目是危险动作，留到有真实需求时再设计（需要先决定级联还是迁移）。
- 「谁的 run 谁改」这类**按人的**约束：与 D24 的"全员可见"取向冲突，不做。

**M7-3 完成小结（2026-09-17，部分）：UI 打磨与只读体验**

**做完了**（每条都有实测）：
- **顶栏导航截断**（用户报的 bug）：根因不是"标签太多"，而是"标题 + 11 个页面 + 项目切换 + 角色 + 账号"约 1550px 挤在 64px 一行里。改成**导航独占第二行** + 收紧菜单项内边距 + 角色挪进账号下拉。无头 Chrome 实测：11 项自然宽 808px，**≥900px 全部标签完整无截断**；760/520px 下不截断而是横向可滚（naive-ui 会压缩菜单项把文字截成"…"，必须 `flex-shrink: 0` + 让菜单自己 `overflow-x: auto` —— 容器上设不生效，因为 `.n-menu` 自带 `overflow: hidden`）。
- **只读账号按钮置灰**：新增 `composables/usePermission.ts`（前端权限统一出口）+ 布局顶部一条只读横幅；六个写页面的按钮 `:disabled`。实测：viewer 在标注工作台 **11 个按钮里 7 个禁用**且横幅可见；admin 同页 **0 个禁用、无横幅**。
- **统一三态**：新增 `components/AsyncState.vue`（骨架屏 / 错误+重试 / 空态三态收敛到一处），语料库与数据集列表已接入 —— 以前"没有数据"和"请求失败"在界面上长得一样。
- **报告导出**：`buildReportCsv`（概况/指标/归因标签/逐题明细四段）+ `buildReportMarkdown`（只放结论级内容），复用对比页的 CSV 转义约定；报告页头部下拉导出；10 条测试（含"没判定显示 — 而不是 0/0/0"这类口径）。
- **菜单防回归**：菜单抽成 `utils/navigation.ts` 纯数据，配 11 条测试做**双向对照**（该进菜单的路由一个都不能漏 / 菜单 key 必须有对应路由 / 按角色过滤 / 项数与标签宽度预警线）。

**未做（转出到下一批）**：其余列表页接入 AsyncState、图表配色统一、标注页快捷键提示打磨、报告页表格列宽再收一遍。

**M7-4 完成小结（2026-09-17）：全栈一键部署**

**交付物**：`compose.yaml`（六服务：postgres / qdrant / **migrate** / api / worker / web）、`server/Dockerfile`、`worker/Dockerfile`、`web/Dockerfile` + `web/nginx.conf`、`scripts/backup.sh`、`docs/deploy.md`、Makefile 的 `deploy-*` 与 `backup`、`.env.example` 部署段。

**关键设计**：
- **migrate 单独成服务**（跑完即退出）：API 运行时不该有改表结构的权限；迁移失败就卡在这一步，而不是让 API 带着半个 schema 起来。`depends_on: condition: service_completed_successfully` 把顺序做成硬约束。
- **api 与 worker 分开打镜像**：依赖与扩缩容诉求完全不同（worker 要 Python + httpx + qdrant-client，且评测密集时只扩 worker）。
- **只对外开一个入口**（nginx）：同源代理 `/api` 就不需要 CORS，前端也不必在构建时写死后端地址；nginx 里 `try_files ... /index.html` 让前端路由刷新不 404。
- **web 镜像里跑 `pnpm build`**（含 vue-tsc）：类型不过 = 镜像构建失败。这条在本次开发中真的拦住过一次（我改到一半时的类型错误直接让镜像构建失败）。
- **备份必须两份配对**：chunk 正文与向量只在 Qdrant（D17），只备 PG 恢复后检索跑不了、只备 Qdrant 则实验记录全无；`make backup` 一次产出 `eval-<时间戳>.sql.gz` + `qdrant-<时间戳>/`。

**真机核对**：`docker compose up -d migrate api web` → migrate `no change`(已在版本 9)、api/web 均 healthy；经 nginx(8090) 验证 `/healthz`、`/`、`/login`、`/runs/155`（SPA 回落）全 200，`/api/v1/auth/status` 通、无 token 访问 `/api/v1/runs` 401；`docker compose run --rm worker python -m app.healthcheck` 全 ok，并用 worker 容器真跑完 **run #156（k=3，60/60 成功，recall 0.9097）** —— 正好补上 k=1(0.6736) 与 k=5(0.9486) 之间那一档，M6 闭环页有了"改配置重跑"的真实候选。

**跑容器才暴露的两个真问题（值得记）**
1. **worker 读的是单条 `PG_DSN`**，不是 `PG_HOST/PG_PORT/PG_USER/PG_PASSWORD` 那一组。按 api 那套配 worker，它会静默回落到默认 DSN（localhost:5432）然后连不上 —— 日志里只有 `connection refused`，很难猜到是变量名不对。健康检查（`app.healthcheck`）把它变成了"启动 5 秒内可见"。
2. **镜像里不该信任宿主机的文件权限**：仓库里有个文件是 `0600`（其他都是 0644，应该是某次受限 umask 创建的），`COPY` 原样保留，于是非 root 的 worker 用户连"读自己代码"都被拒（`PermissionError: /app/app/generation/__init__.py`）。镜像里统一 `chown -R worker:worker /app && chmod -R a+rX /app` 兜底。

**未做/转出**：HTTPS/域名与反向代理（`docs/deploy.md` 里给了指引，没做成 compose 里的服务）；镜像发布到私仓（现在是本地 build）；多副本 worker 的编排示例。

---

---

## 7. 目录结构与提交规范

### 目录结构（目标形态，随里程碑生长）

```
eval-platform/
├── compose.yaml
├── Makefile
├── .env.example
├── README.md
├── process.md              # 本文档，随进展更新
├── docs/                   # ADR 决策记录、故障演练、部署文档
├── datasets/               # 评测集资产（语料原始件 + jsonl + 标注脚本）
├── server/                 # Go API
│   ├── cmd/api/main.go
│   └── internal/
│       ├── http/           # Gin handlers + middleware(auth/rbac)
│       ├── store/          # PG 访问与迁移
│       ├── queue/          # job 领取/状态（服务端侧）
│       └── domain/         # 领域逻辑（数据集校验/run 编排/报告聚合）
├── worker/                 # Python 评测执行
│   ├── pyproject.toml
│   └── app/
│       ├── runner/         # case runner（retrieval/generate/judge 编排）
│       ├── retrieval/      # chunker / embed / qdrant / anchor 映射 / rerank
│       ├── judge/          # 协议、prompt 模板、claim 分解、entailment
│       ├── adapters/       # builtin / http(外部被测服务)
│       ├── metrics/        # 指标计算与聚合
│       └── db/ queue/      # job 领取、checkpoint、心跳（worker 侧）
└── web/                    # Vue3 + TS
    ├── src/views/          # login/datasets/corpora/runs/reports/compare/annotate/admin
    └── src/api/ components/ store/
```

### 工作流规则（AGENTS.md 约定 + 本项目补充）

- 每个任务卡 = 一次改动；**先写/改测试 → 全绿 → 一个 commit**。
- commit message 规范：`<type>: <subject>`，type ∈ feat/fix/refactor/test/docs/chore；必要时正文说明动机。
- 里程碑完成 = 该节任务全勾 + 验收 demo 可跑 + 更新 process.md（勾选/记录偏差/新增 ADR）+ 一个 docs commit + 一个 tag（`v0.x.0-m<里程碑>`）。
- tag 现状：`v0.1.0-m0` / `v0.2.0-m2` / `v0.3.0-m3` 已有；**M4 与 M5 的收尾落在同一个 commit（`6b8aedc` A/B对照，M4-4.1 与 M5-1/M5-2 一起进来），因此 `v0.4.0-m4` 与 `v0.5.0-m5` 指向同一个 commit**；M6 的 tag 待 M6 收口 commit 打上。
- 依赖注入与配置不写死；所有 secret 走 `.env`。

---

## 8. 风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| LLM-as-Judge 不稳定/有偏差 | 指标不可信 | 固定协议 + 金标校准（M6）；对比时顺序随机；rubric 写死 |
| 语料与标注工作量大 | 进度拖最久 | 先 30–100 题起步；出题与标锚点同步做；明确自检清单 |
| LLM 成本/限流 | 跑不动大批量 | smoke run 先行；judge 缓存；token 落库核算成本；并发限流（D8） |
| 外部被测服务契约漂移 | adapter 失效 | 契约先冻结（D3）；adapter 层独立 + 契约测试 |
| 环境差异不可复现 | 对比失真 | 配置快照 + hash + git_sha + judge 版本四件套（D7） |
| 向量库/索引随切分参数错乱 | 指标全错 | collection 按 chunking_hash 隔离命名；锚点映射失败即报错（D1） |
| API key 泄露 | 安全事故 | 只进 .env；示例文件用占位符；README 警示 |
| 同事共用互相污染/误删 | 数据事故 | project 隔离 + RBAC + 删除二次确认（D9） |
| 范围膨胀（想做太多） | 烂尾 | 以里程碑验收为准砍范围；M2 先出数再谈优化 |

---

## 9. 开工前待拍板清单

> 多数不影响 M0–M1 骨架，但建议尽早定，避免 M2 返工。

1. **语料领域**：~~第一个领域语料选什么？~~ ✅ **已定**：先落地本地 mock 中文语料《知简 CRM》帮助文档体系（`datasets/corpus/`，21 篇 .md，带 doc_id frontmatter 与稳定小标题，虚构产品无版权顾虑）。后续换真实领域语料时保持目录结构替换即可，评测平台代码不感知语料来源。
2. **LLM/embedding/reranker 供应商与预算**：✅ **已定**：生成/judge 用 **DeepSeek**（`https://api.deepseek.com`，OpenAI 兼容；chat=`deepseek-chat`，推理=`deepseek-reasoner`），架构支持多厂商 + 成本/延迟/难度路由（D-D / D11）。**embedding 用 SiliconFlow `BAAI/bge-m3`（维度 1024，已实测连通，直连可用）**，rerank 后续同源可选（如 `BAAI/bge-reranker-v2-m3`）。Key 只放 `.env`（gitignored），`.env.example` 放占位符；如密钥曾在对话/日志出现需轮换。
3. **是否有现成线上 RAG/Agent 可作 HTTP adapter 的真实被测对象**：有 → 契约按它校准；没有 → M2–M4 先用 builtin 模式，M7 前再接入。
4. **部署形态**：同事共用是"内网一台机器 docker compose"还是云服务器？影响 M7 部署文档与鉴权强度。
5. **评测集规模预期**：中期想扩到多少题、是否多领域（决定 datasets/cases 是否需要更重的组织方式）。
   → **进展（2026-09-11）**：v2 已扩到 **60 题**（dataset 4，补齐 A01/A02/C04/E01/E02/E03 六个盲区），锚点覆盖率 100%。当前规模下 k=5 的 Recall 已接近饱和（55/60 题满分），**扩量的优先级低于"加难题"**：M6 视需要扩到 100+ 时，优先补"跨文档/同义改写/干扰对照"三类题（提分空间在 k=1/3 与 MRR，见 M3 小结）。多领域暂不做，先把单一领域的评测深度做透。
6. **登录方式**：自建账号密码足够，还是需要对接公司 SSO/企业微信？（默认先自建，SSO 留扩展位）

---

## 10. 与 agent 的协作方式

> 你已明确：动手时**由你逐步实现**，agent 负责给"下一步做什么"、评审与解卡。约定如下。

- **任务卡**：每个动手轮次开始，agent 产出一张任务卡，固定字段：
  - 目标（一句话）+ 依赖（前置条件）
  - 改动文件清单（精确到路径）
  - 验收标准（可执行/可演示）
  - 测试要求（哪些单测/集成测，覆盖什么）
  - 建议 commit 信息
- **你的职责**：按卡实现；每步跑测试并提交；遇到卡点把报错/现象贴给 agent。
- **agent 的职责**：给卡、答疑、评审 diff 与测试覆盖、给"这步为什么这样设计"的讲解、必要时给参考代码片段；**不代写整块功能**。
- **本文档的维护**：每个里程碑完成 → 更新 process.md（勾选完成项、记录与规划的偏差、把新的设计结论沉淀为 ADR 放进 docs/），再开始下一里程碑。
- **"完成"的定义**：任务卡验收标准达成 + 测试全绿 + commit 已落 + demo 可跑。缺一不算完成，不进入下一步。

> 起点：M0 之前，先回复 §9 的待拍板项（尤其是 1 和 2），然后从 M0 任务清单第一项开始。
