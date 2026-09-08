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

---

## 3. 核心领域模型与数据表

> 表结构在 M1 落地第一版，后续里程碑按需加列/加表，禁止猜一堆用不上的字段。
> chunk 向量本体在 Qdrant；PG 只存 chunk 的元信息（映射关系、chunking 指纹）。

| 表 | 用途 | 关键字段 |
|----|------|----------|
| `users` | 登录、角色 | email/username, password_hash, role(admin/editor/viewer) |
| `projects` | 数据隔离命名空间（同事共用） | name, created_by |
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
| `annotations` | Bad Case 人工标注 | case_result_id, annotator_id, tag, comment, status(open/fixed/verified) |
| `human_gold_scores` | 人工金标（校准 judge 用） | case_id, annotator_id, metric, score, jsonb |
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

**任务清单**
- [ ] git 仓库规范（main 分支、提交信息规范、.gitignore 含 `.env`）
- [ ] 目录骨架落地（§7 结构）+ README 开头
- [ ] `docker-compose.yaml`：postgres + qdrant（+ 可选 redis），健康检查与卷
- [ ] Go API 骨架：Gin + 配置加载 + 日志 + `/healthz`（含 PG/Qdrant 连通检查）
- [ ] golang-migrate 接入，首个空 migration + `make migrate`
- [ ] Python worker 骨架：pyproject + 依赖分组(dev/runtime) + 日志(structlog) + 连 PG/Qdrant 的最小脚本
- [ ] Makefile：`up-deps/api/worker/web/migrate/test`
- [ ] 前端脚手架：Vue3+TS+Vite+Naive UI+路由+布局空壳（登录页占位）

**验收标准**：新机器 clone 后按 README 三步能起依赖并访问 API 健康页；`make test` 在各端有占位测试跑通。

**测试与提交**：Go handler 冒烟测试、Python 连通性测试（pytest，连不上则 skip）；每步一个 commit。

---

### M1 语料与数据集建模（数据层 MVP）

**任务清单**
- [ ] PG 迁移：§3 表中 `projects/users/corpora/documents/datasets/cases`（不含后续实验表）
- [ ] Go：三组 REST API —— 语料库 CRUD、文档上传（文本/文件）、数据集与 case 管理（导入 jsonl、列表、详情、删除）
- [ ] case 导入校验：qid 唯一、gold_anchors 引用存在的 doc、必填项
- [ ] 前端：语料库 + 数据集 + case 列表三页（表格、导入对话框、表单），遵循 D9 设计基线
- [ ] 中文样例语料 & 30 题评测集落地（见 §5）→ 入库
- [ ] 单元/集成测试覆盖导入校验主路径

**验收标准**：网页上传中文文档、导入 30 题 jsonl、查得到、改得到、错误导入有明确报错。

---

### M2 内置检索评测闭环（第一份真实指标）

> 全项目第一个"出数"的里程碑。不碰 LLM，纯检索 + 确定性指标，先建立可信基线。

**任务清单**
- [ ] Qdrant collection 管理：按 (语料, chunking_hash) 命名/隔离；payload 存 doc_id/span 映射/chunk 文本
- [ ] chunker（内置实现，含"按标题层级/段落/固定大小"等策略）+ chunking_hash 记录（D7）
- [ ] embedding 供应商接入（OpenAI 兼容封装，先接一个，见 §9）
- [ ] 锚点映射器：gold anchor 区间 → 当前 chunk 集合 `G`（D1），含映射失败校验
- [ ] 检索器：top-k 查询 + 可选手工 rerank（M2 先不做 reranker 也 OK）
- [ ] **最简评测 runner**（先同步跑通，M3 再异步化）：对每题取 `Rk`，算 Recall@k/Precision@k/MRR@k/Hit@k（口径 §D10）
- [ ] runs/jobs/job_items/case_results 表落地 + 结果落库（为 M3 打底）
- [ ] 报告 API + 前端报告页雏形：指标卡片 + 每题明细表
- [ ] Python 侧指标计算单元测试（构造小样例手算核对）+ chunker/锚点映射测试

**验收标准**：用评测集跑出可信指标；**换一个明显更差的检索配置（如 top_k=1）指标随之下降** —— 证明系统"能测出好坏"，这一步是简历 demo 的起点。

---

### M3 异步任务基建（可靠性核心）

> 面试含金量集中点：持久化队列、checkpoint 续跑、心跳、重试、死信。

**任务清单**
- [ ] job 领取协议：worker 轮询 `FOR UPDATE SKIP LOCKED`；job 状态机 `pending→running→succeeded/failed`，job_items 状态机细化
- [ ] **checkpoint 提交**：逐条 case 独立事务写 `case_results` + 推进 `job_items`（D2）
- [ ] 心跳与超时接管：`heartbeat_at` 刷新；worker 端恢复逻辑=扫描 running 且心跳过期的 job_items 重新入队
- [ ] 重试与退避：瞬时错误按类型重试（`retry_count` 上限）→ 超过进 failed 并记录原因；不吞错
- [ ] 进度：job.progress 汇总 + API 轮询 + WebSocket 推送（前端进度条/日志面板）
- [ ] 把 M2 的同步 runner 迁入 worker；并发上限可配
- [ ] 故障演练文档 + 脚本：跑一半 `kill -9` worker → 重启 → 续跑只补剩余 case
- [ ] 集成测试：多 worker 并发领取不重复执行（幂等断言）、中断续跑不重算、死信路径

**验收标准**：50+ 题 run 进行中杀 worker，重启后从断点续跑完成，结果与全量跑一致；前端能看到实时进度。

---

### M4 生成侧评测 v1（Judge 与幻觉归因）

**任务清单**
- [ ] 生成链路（builtin）：调生成 LLM 产出 answer，记录 prompt id/模型/温度到配置快照
- [ ] judge 协议 v1（自研 prompt + 严格 JSON 输出 + 解析校验）：
  - [ ] claim 分解器（D4）
  - [ ] claim × 检索上下文 entailment 判定（supported/unsupported/irrelevant）
  - [ ] relevance/helpfulness rubric 打分（中文 rubric）
- [ ] **归因规则落地**（§D10）：自动给 case 打 `retrieval_miss / hallucination / generation_quality / context_missing`
- [ ] judge 缓存（D5/D8）；token 用量落库
- [ ] 报告扩展：生成侧指标卡片 + 每题详情含 answer/claims/判定 + 幻觉文本高亮
- [ ] 前端：case 详情抽屉（可折叠展示检索上下文 vs answer vs claims）
- [ ] 测试：judge 解析异常重试、claim 分解与判定的小样例、归因规则单测、缓存命中不重调用

**验收标准**：跑 30 题能看到"幻觉率 X%、召回问题 N 条、幻觉 M 条"，且能点开看**具体是哪句答案在编**。（先接受 judge 可能不完美，M6 校准。）

---

### M5 实验管理与 A/B 对比报告

**任务清单**
- [ ] `pipeline_profiles` 配置模板管理（CRUD）+ 校验（引用的语料/模型存在）
- [ ] 配置快照与指纹：run 建立时固化 `config_snapshot` + `config_hash` + `git_sha`（D7）
- [ ] run 列表/详情：同数据集多次 run 的历史与状态
- [ ] **A/B 对比**：选择同一数据集的两次 run → per-question 差值表 + Wilcoxon p 值（scipy）+ 分层（category/difficulty/flag）对比（D6）
- [ ] 报告导出：差值表 CSV/JSON/Markdown
- [ ] 前端：配置模板表单（分组/可折叠）、对比页（并排指标 + 差分布图 + 分层表）
- [ ] 测试：config_hash 稳定性（同配置同 hash）、A/B 计算正确性（小样例手算）、导出格式

**验收标准**：用同一评测集跑 2–3 组不同配置，一键产出"谁在哪些题上赢"的对比报告；hash 复现定位历史 run 可演示。

---

### M6 Bad Case 标注与金标校准闭环

**任务清单**
- [ ] 标注工作台：从报告/flag 过滤进入 → 打标签 + 评论 + 状态流转 `open→fixed→verified`（annotations 表）
- [ ] "bad case → 归因建议"：自动给出检索/幻觉/生成建议（基于 flag 与 judge 判定），人工确认或纠正
- [ ] 人工金标集流程：从评测集选 30–50 题，双人/单人 + 复核打分入 `human_gold_scores`
- [ ] **judge 校准报告**：自动评测 vs 人工金标的一致性（κ / 一致率 / 混淆），暴露 judge prompt 缺陷
- [ ] 迭代闭环支撑：改 profile 配置 → 重跑 → 对比"上一版 vs 这一版"中已标 fixed 的 case 是否变好
- [ ] 前端：标注面板（快捷键友好）、金标打分页、校准报告视图
- [ ] 测试：标注状态机、校准指标计算、闭环对比查询

**验收标准**：完整 demo —— 发现一批 bad case → 打标归类 → 改配置重跑 → 报告显示该批指标回升且对应 case 状态推进到 verified。

---

### M7 多用户、打磨与部署交付（同事可用）

**任务清单**
- [ ] 登录/注册 + JWT + RBAC（admin/editor/viewer，权限落到 API 与前端路由）
- [ ] project 隔离完善：成员管理、资源归属、跨 project 只读隔离校验
- [ ] UI 打磨：设计基线统一（空/载/错状态、表格、图表配色）、报告导出按钮、标注页体验
- [ ] Docker Compose 一键部署（api+worker+web+pg+qdrant+可选 redis），`.env.example` 完整注释
- [ ] 部署文档：内网机器三步启动、备份（PG dump + Qdrant snapshot）、升级流程
- [ ] README 成稿：项目动机、架构图、指标口径、**参考调研章节（DeepEval/RAGAS 等对比与借鉴点，注明核心自研）**、demo 脚本
- [ ] 全量测试回归 + 演示录制脚本（一步步讲：上传→评测→报告→对比→标注）
- [ ] 找同事试用一轮，收集 3–5 个真实反馈并修掉高优项

**验收标准**：同事在部署环境注册登录，能独立完成"建项目→导入数据→跑评测→看报告→标 Bad Case"全流程；README 能支撑你面试讲 20 分钟。

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
- 里程碑完成 = 该节任务全勾 + 验收 demo 可跑 + 更新 process.md（勾选/记录偏差/新增 ADR）+ 一个 docs commit。
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
2. **LLM/embedding/reranker 供应商与预算**：~~judge 与生成用哪家……~~ ✅ **部分已定**：先接 **DeepSeek**（`https://api.deepseek.com`，OpenAI 兼容；chat=`deepseek-chat`，推理=`deepseek-reasoner`），架构支持多厂商 + 成本/延迟/难度路由（D-D / D11）。**待定：embedding/rerank 供应商**——DeepSeek 无此类接口，M2 检索侧需要第二家（候选：SiliconFlow 的 bge-m3 / 阿里云 text-embedding；rerank 同源可选）。M2 前申请到即可，不影响 M0/M1。
3. **是否有现成线上 RAG/Agent 可作 HTTP adapter 的真实被测对象**：有 → 契约按它校准；没有 → M2–M4 先用 builtin 模式，M7 前再接入。
4. **部署形态**：同事共用是"内网一台机器 docker compose"还是云服务器？影响 M7 部署文档与鉴权强度。
5. **评测集规模预期**：中期想扩到多少题、是否多领域（决定 datasets/cases 是否需要更重的组织方式）。
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
