# Eval Platform — 中文 RAG / Agent 评测平台

**给 LLM 应用做"可信、可复现、能闭环"的自动评测**：上传中文语料与评测集（带 gold 锚点）→ 批量异步跑检索 / 生成 / 判定 → 出检索与生成两侧指标 → A/B 对比定位"哪几道题变好/变坏" → Bad Case 标注 → 人工金标校准 judge → 改配置重跑 → 验证销单。

**核心评测逻辑自研**（指标、judge 协议、归因规则、统计检验都在本仓库里，Go 与 Python 各一份纯函数实现 + 真库测试），不套 DeepEval / RAGAS 的外壳 —— 理由与借鉴点见 [参考调研](#参考调研deepeval--ragas-等框架的对比与借鉴)。

真实数据（自建中文语料 80 chunk / 评测集 60 题，非玩具规模）：

| 实验 | 配置 | Recall@k | MRR@k | 说明 |
|------|------|---------|-------|------|
| run #112 | top_k=1 | 0.6736 | 0.7833 | 检索不足的基线 |
| run #156 | top_k=3 | 0.9097 | 0.8806 | 只调一个变量 |
| run #100 | top_k=5 | 0.9486 | 0.8839 | #112 vs #100：**+27.5pp**，Wilcoxon `p=2e-06`，**修好 19 题 / 变坏 0 题** |
| run #155 | top_k=5 + 生成 + 判定 | 0.9486 | 0.8839 | 60 题全部判定，幻觉率 0.0%，judge 花了 101k tokens |

---

## 目录

- [为什么做这个](#为什么做这个)
- [架构](#架构)
- [快速开始](#快速开始)
- [核心能力](#核心能力)
- [指标口径](#指标口径)
- [参考调研：DeepEval / RAGAS 等框架的对比与借鉴](#参考调研deepeval--ragas-等框架的对比与借鉴)
- [复现性：一次实验的四件套](#复现性一次实验的四件套)
- [20 分钟 demo](#20-分钟-demo)
- [工程实践](#工程实践)
- [已知限制与 Roadmap](#已知限制与-roadmap)
- [目录结构](#目录结构)

---

## 为什么做这个

现成的评测框架（RAGAS / DeepEval 等）解决的是"**这条输出好不好**"：给一个 test case，返回 0~1 的分数加一段理由。但把它接到一个真实的中文 RAG 项目上，还会卡在四个问题上：

1. **检索侧不该用 LLM 当裁判。** 有 gold 锚点时，"该召回的证据有没有召回到、排在多后"是**集合运算**，可精确计算、零 token、可复现；用 LLM 判 context precision 既贵又抖。
2. **结论必须能追到"哪个环节坏了"。** 光说"这题分数低"没法行动；要说"这题是**检索漏召回**，不是模型在编"——这决定了下一步是调 k、改切分，还是改 prompt。
3. **"这次实验"的配置必须固化成凭据。** 同一个问题，换一次 run 答案就变了；没有快照/指纹，A/B 对比就是两个未知配置的对比，结论不可复现。
4. **评测要接回迭代流程。** 发现 bad case → 打标 → 改配置 → 重跑 → 验证销单，这条闭环走通，"评测"才不是一次性的报告。

这个平台就是按这四个问题设计的：**确定性优先、口径可解释、实验可复现、闭环可销单**。

---

## 架构

```
┌─────────────────────────────────────────────────────────────────────────┐
│  web (Vue3 + Naive UI + TS)   控制台: 语料/数据集/报告/对比/标注/金标/闭环   │
└───────────────┬─────────────────────────────────────────────────────────┘
                │ /api/v1  (JWT + RBAC: viewer / editor / admin)
┌───────────────▼───────────────────────┐        ┌──────────────────────┐
│  server (Go + Gin)                    │        │  PostgreSQL 16       │
│  · 业务 CRUD / run 编排 / 报告聚合     │◄──────►│  15 张表 / 9 个迁移   │
│  · 配置快照 + config_hash (可复现核心) │        │  runs·case_results   │
│  · A/B 对比计算 (自研 Wilcoxon)        │        │  annotations·gold    │
│  · 归因/校准的读接口                   │        └──────────────────────┘
└───────────────┬───────────────────────┘        ┌──────────────────────┐
                │ 任务落库入队 (jobs / job_items) │  Qdrant v1.19        │
                │ 异步、可中断续跑                 │  向量 + chunk 正文    │
┌───────────────▼───────────────────────┐◄──────►│  (正文不落 PG, D17)  │
│  worker (Python)                      │        └──────────────────────┘
│  · 检索: embed → search → 锚点映射     │
│  · 生成: builtin LLM 或外部被测服务    │
│  · 判定: claim 分解 + 逐条 entailment  │        ┌──────────────────────┐
│  · 指标: recall/MRR/hit/precision      │───────►│  LLM API             │
│  · 归因: 9 类标签 + 主因 (可离线重算)   │        │ (SiliconFlow/DeepSeek)│
└───────────────────────────────────────┘        └──────────────────────┘
```

**两条关键数据流**

1. **提交评测**：`POST /runs` 只做三件事 —— 把配置固化成快照并算指纹、建 run + job + 每题一条 `job_items`、立即返回。执行完全交给 worker，HTTP 请求不等评测跑完。
2. **逐条 checkpoint**：每完成一道题，在**同一个事务**里写 `case_results` + 推进 `job_items` + 重算 `jobs.progress`。崩在事务内则该题重跑，崩在事务间则已完成的不重算 → 语义上 at-least-once，**结果上 exactly-once**。

---

## 快速开始

### A. 部署（给同事用；只需 Docker）

```bash
cp .env.example .env
make jwt-secret              # 输出填进 .env 的 JWT_SECRET=
# 再填 SILICONFLOW_API_KEY（embedding / generation / judge 都用它）
make deploy-up               # 构建 + 迁移 + 起六个服务
# 打开 http://<机器IP>:8080 → 首次打开引导创建管理员
```

详见 [docs/deploy.md](docs/deploy.md)（端口与入口 / 备份与恢复 / 升级 / 排障表 / 上内网前的安全清单）。

### B. 本地开发（改代码；前后端在宿主机跑，便于热重载）

```bash
make up-deps                 # postgres + qdrant
make migrate-up              # 建表到最新（9 个迁移）
make api-restart             # Go API → :8080（按端口杀旧实例 + 等 healthz）
make worker-setup            # 首次：建 venv 装依赖
make web-install && make web # 前端 → :5173
```

前置：Docker Desktop、Go 1.24+、Python 3.11+（开发机 3.14）、Node 20+ 与 pnpm。

> **第一次跑评测会发生什么**：上传文档后直接点「提交评测」即可 —— worker 发现该语料在这个切分配置下还没有索引，会**按本次 run 的快照配置自动建一次索引**（日志里 `index_build_start` / `index_build_done` 写清 docs/chunks/embed_tokens/耗时），之后同配置的 run 直接复用，不会重复 embedding。想提前批量建索引（灌大数据集时更省心）可以手动跑：
> `cd worker && .venv/bin/python -m app.cli.index_corpus --corpus-id <id> [--recreate]`（见 process.md D25）。

---

## 核心能力

### 1. 检索侧指标：确定性、零 token

评测集每题带 **gold 锚点**（`doc_id` + 可选 `span`）。锚点 → chunk id 的映射在入库时算好并落库，所以召回率是纯集合运算：`recall@k = 命中 gold 数 / gold 总数`，另有 `MRR@k`（第一个 gold 的排名倒数）、`Hit@k`、`Precision@k`。**不调 LLM，零成本、可复现、可离线重算**。

### 2. 生成侧 + judge：claim 级事实核查

判定不直接问"这段答案有没有幻觉"（这种问法模型很难稳定），而是两步：

1. **claim 分解**：把答案拆成可核查的原子断言（`max_claims` 上限，超出的标注 `truncated_claims`）；
2. **逐条判定**：每条断言对检索上下文标 `supported / unsupported / irrelevant`，附 `evidence`（引用原文）与 `reason`。

再由 rubric 给 relevance / helpfulness 打 1–5 分（v2 加了硬规则：**存在 unsupported 断言则 helpfulness ≤ 2**）。三个率（支持率 / 幻觉率 / 无关率）分母都是 `claims_total`，**三率之和恒为 1**——报告页拿这条做自洽校验。

成本与可复现：judge 结果按 `(模型, prompt 版本, 输入指纹)` 缓存（本仓库缓存表里已有 120 条判定结果），token 用量分生成/判定两侧落库；换 prompt 版本会换缓存键，不会串。

### 3. 归因：从"分数低"到"哪个环节坏了"

每题打一组**环节事实标签**，`flags[0]` 即主因（D16），9 类优先级从上游到下游：

```
no_gold > anchor_incomplete > retrieval_miss > retrieval_partial > retrieval_low_rank
        > hallucination > off_topic > generation_quality > no_claims
```

关键设计：**规则是纯函数、零 I/O**，既能被队列落库，也能被 CLI 对历史 run 重算 —— 规则升级不需要重跑 LLM。实测 run #155 一次判定花 101k judge tokens，而归因重算 **0 token**。

### 4. A/B 对比：三条证据并排，而不是一行 p 值

| 证据 | 回答的问题 | 实测（#112 k=1 → #100 k=5） |
|------|-----------|---------------------------|
| 逐题差值 + 聚合 | 差了多少 | recall 0.6736 → 0.9486（+27.5pp） |
| 噪声底 + Wilcoxon | 是不是碰运气 | 跨 run 噪声底中位 3e-4 / 最大 2.1e-3；`p=2e-06`（仅作旁证） |
| 翻转题清单 | 具体哪几道 | 修好 19 题 / 变坏 0 题 / 标签变化 1 题 |

**为什么不信单一 p 值**：检索指标离散且大量并列（60 题里 55 题满分），Wilcoxon 的正态近似在大量 ties 下不可靠。所以结论以"跨 run 噪声底 + 翻转题清单"为准，p 值只用于旁证 —— 这条写进了决策记录（D18）。Wilcoxon 是自研实现（不引 scipy，避免为一个检验把 Python 科学计算栈带进 Go 侧的对齐成本）。

### 5. 标注闭环：从待办到销单

`open → fixed → verified`（外加 `wontfix`）状态机在**服务端**（DB 的 CHECK 只管枚举，业务规则不写进迁移）。闭环页回答一个问题：**我标成 fixed 的题，在新一次 run 里真的好了吗？**

- 五种结局：`improved / stable / worsened / changed / unverifiable`，每条都带**指标级证据**（`recall 0% → 100%`）；
- 只有"状态是 fixed **且** 确实变好"才允许销单（`verified`），否则给一句"为什么还不能销单"；
- 护栏：两次配置指纹相同 = 这是复现不是实验；候选 run 未做过归因 = 整体判为不可判断（**绝不报"全修好了"**）。

### 6. judge 校准：判官本身可信吗

用人工金标（`human_gold_scores`，按 `(run, case, annotator)` 唯一）回答三件事：

- **金标本身可信吗**：双人独立打分的一致性（inter-annotator）；
- **judge 与人工在"有没有幻觉"上一致吗**：混淆矩阵 + 一致率 + **Cohen's κ**（扣掉"瞎猜也能对"的部分——幻觉率低时，一个永远说"没有幻觉"的判定也能拿 90% 一致率，κ 会把它打回 0）；
- **分数差多少、往哪偏**：MAE / 完全一致率 / ±1 一致率 / 均值偏差（带符号，能看出 judge 偏宽松还是严格）。

报告还会自曝"别信我"的条件（样本 < 20 题、金标覆盖率 < 50%、只有一位标注员且未复核），并给出**判错清单**（漏判在前——那类错会让幻觉流出线上）。

---

## 指标口径

| 指标 | 公式 / 定义 | 为什么这么定 |
|------|------------|-------------|
| `recall@k` | 命中 gold chunk 数 / gold 总数 | 锚点是集合，不劳 LLM |
| `MRR@k` | 1 / 第一个 gold 的排名 | 排序问题的唯一敏感指标（k=5 时 recall 已饱和，MRR 才有区分度） |
| `Hit@k` | 至少命中一个 gold | 粗粒度可达性 |
| `Precision@k` | 命中数 / k | 上下文噪声（太高说明塞了无关 chunk） |
| `claim_support_rate` | supported / claims_total | 分母是断言数，不是题数：没断言的题不进样本 |
| `hallucination_rate` | unsupported / claims_total | 三率之和恒为 1（报告页硬校验，容差 1e-5） |
| `helpfulness_avg` | rubric 均值（1–5） | 与幻觉率交叉看：**检索到位、无幻觉，但答不到点上**是另一类问题 |
| 归因标签 | 见上文优先级表 | `flags[0]` = 主因，同一个 run 里标签与阈值一起落库（可解释） |
| 噪声底 | 同配置重复跑的最大偏差（2.1e-3） | 差值小于它一律不宣称"变好/变坏" |
| κ | `(po − pe) / (1 − pe)` | 扣掉随机一致；pe=1（无变异）时无定义，返回 0 而非 1 |

**两条贯穿全项目的口径纪律**：①「没有判定」≠「零幻觉」（judge 没跑到、claims 为空时**不写率值**，而不是写 0）；②「落在噪声里」≠「有变化」（差值小于噪声底一律不涂色、不下结论）。

---

## 参考调研：DeepEval / RAGAS 等框架的对比与借鉴

写这个平台之前对比了两个主流框架的公开文档（[RAGAS 指标清单](https://docs.ragas.io/en/stable/concepts/metrics/available_metrics/)、[DeepEval 指标导论](https://deepeval.com/docs/metrics-introduction)）。它们的覆盖面远比我宽（DeepEval 自称 50+ 指标，含 agent 轨迹、多轮对话、安全与红队），我**刻意只做其中一小块**，原因是本项目要解决的是"接到真实项目上的最后一公里"，而不是"指标能不能更多"。

| 维度 | RAGAS | DeepEval | 本平台 |
|------|-------|----------|--------|
| 定位 | RAG 指标库 + 测试集生成 + prompt 优化 | pytest 式 LLM 评测框架 + 平台（Confident AI） | **可复现的实验平台 + 迭代闭环** |
| RAG 指标 | Context Precision/Recall、Context Entities Recall、Noise Sensitivity、Response Relevancy、Faithfulness | Retriever: Contextual Relevancy/Precision/Recall；Generator: Answer Relevancy、Faithfulness | **gold 锚点的确定性 recall/MRR/Hit/Precision** + claim 级三率 + rubric |
| 判定方式 | LLM-as-judge 为主 | LLM-as-judge（G-Eval / DAG / QAG），分数 0–1 + 理由 + 阈值 | 自研两段协议：claim 分解 → 逐条 entailment；阈值口径落库 |
| 根因定位 | 弱（给分数） | 弱（给分数与理由） | **9 类环节标签 + 主因 + 可离线重算（0 token）** |
| 实验管理 | 无（跑脚本） | 无（跑测试；平台侧有对比） | **config_snapshot + config_hash + git_sha**，A/B 可比性校验 |
| A/B 统计 | 无 | 无 | 逐题配对差值 + 噪声底 + 自研 Wilcoxon + 翻转题清单 |
| 可靠性 | 同步脚本 | 同步（pytest） | **异步任务 + 逐条 checkpoint + 僵尸接管 + 死信** |
| judge 可信度 | 无 | 无（可选择更强模型） | **人工金标校准：κ / 混淆矩阵 / MAE / 双人一致性** |
| 迭代闭环 | 无 | 无 | **标注状态机 + 销单 + 改配置重跑** |
| 成本核算 | Token 用量可选 | 有 token 统计 | 生成/判定两侧 token 与缓存命中都落库 |
| 测试集生成 | 有（知识图谱 / 场景生成） | 有（Golden Synthesizer / 对话模拟） | **不做**：自建中文评测集 + gold 锚点（锚点比合成题更贴业务） |
| Agent / 多轮 / 安全 | Agent 与工具指标；无安全 | 轨迹/工具/多轮/安全/多模态 + DeepTeam 红队 | **不做**（当前评测对象是单轮 RAG 链路），见 Roadmap |

### 借鉴了什么

1. **faithfulness 的分解思路**（RAGAS）：不问"整体有没有幻觉"，而是拆成断言逐条核查。我的 `claims → supported/unsupported/irrelevant` 与它的 faithfulness 分解同源，但把判定结果**落库并复用**（换 prompt 才换缓存键）。
2. **"分数 + 理由 + 阈值"的输出形态**（DeepEval）：单一分数不可行动，必须能看到理由。我的 rubric 是 1–5 分 + `reason`，并加了达标线（`quality_line`）用于归因的"生成质量"标签。
3. **context precision / recall 的问题意识**（RAGAS）：它用 LLM 判上下文质量，我用 gold 锚点算确定性版本。**同一个问题，换了实现路线**——这也是本项目最大的取舍（见下）。
4. **CI 集成的形态**（DeepEval 的 pytest 风格）：`make verify` / `make test-live` / 演练脚本用**退出码**表达结论（`make drill-compare REF=100` 退出码 0 即逐题一致），便于挂到流水线。

### 为什么核心自研（而不是套壳）

1. **可复现性框架层不管**：RAGAS/DeepEval 都不解决"这次实验的配置是什么、能不能复现"。而这恰恰是 A/B 结论成立的前提 —— 所以快照 + 指纹 + git_sha 由本平台自己管（进程内跨语言哈希一致：Go 与 Python 对同一份配置算出**逐字相同**的 `config_hash`）。
2. **根因是自研的重点**：指标只有落到"哪个环节坏了"才能指导下一步动作。归因规则做成纯函数，既能实时打标也能离线重算历史 run（规则升级 0 token），这条在框架里没有对应物。
3. **可靠性需求不同**：框架是同步脚本，60 题跑到一半崩了就重来；本平台的核心是异步任务基建（checkpoint / 心跳接管 / 死信重试），因为评测是**分钟级、花钱、要能续跑**的作业。
4. **口径要能被自己解释**：指标在 Go 与 Python 各有一份实现，靠**真库集成测试**保证两侧一致（而不是"框架说它是对的"）。评审能问到的每一处口径，仓库里都有对应决策记录（`process.md` 的 D1–D24）。

### 不做什么（以及为什么）

- **不做红队 / 安全指标**（Bias、Toxicity、PII Leakage 等）：不在当前目标内，需要时可用 DeepEval/DeepTeam 补。
- **不做合成测试集生成**：本项目走"自建中文评测集 + gold 锚点"路线 —— 锚点让检索侧可精确计算，这是合成题给不了的。
- **不做多轮 / Agent 轨迹指标**：当前评测对象是单轮 RAG 链路；要做需要先定义"轨迹"的数据模型，属于下一阶段（Roadmap）。

---

## 复现性：一次实验的四件套

任何一份报告都能被复现，靠四样东西同时落库：

| 要素 | 落地位置 | 作用 |
|------|---------|------|
| `git_sha` | `runs.git_sha` | 代码版本 |
| `config_snapshot` + `config_hash` | `runs` 表 | 完整配置（切分/embedding/top_k/生成/判定参数）；哈希用于比对"是不是同一场实验" |
| `dataset_id` + `corpus_id` + `chunking_hash` | 快照 + `case_results` | 数据与切分版本（`chunking_hash` 还决定 Qdrant 集合名 `corpus{id}_{前8位}`） |
| 模型与 prompt 版本 | 快照内 `generation` / `judge` 段 | 换模型或 prompt 必然换指纹 |

配套纪律：**配置只在启用时进快照**（只跑检索的 run 快照里没有 `generation`/`judge` 段，D14）；**归因阈值与规则版本进 `runs.metrics.attribution`**（不进快照，避免历史 run 指纹失配，D15）。

---

## 20 分钟 demo

有个可执行的版本：**`scripts/demo.sh`**（或 `make demo`）。它会**每一步先打"预期现象"再打真实返回值**并当场对比，所以录屏不用背台词；默认走下面第 1–5 步（不花 token），加 `--full` 会真的从零建项目/传文档/跑两次纯检索评测（只花 embedding，不烧生成与判定 token）。

```bash
DEMO_EMAIL=you@example.com DEMO_PASSWORD=*** scripts/demo.sh          # 演示路径
DEMO_EMAIL=you@example.com DEMO_PASSWORD=*** scripts/demo.sh --full   # 从零跑一遍
```

手动走的话按下面这个顺序，刚好覆盖"发现 → 定位 → 修 → 复测 → 销单"：

```bash
# 0) 起服务（见"快速开始"）
make up-deps && make migrate-up && make api-restart && make web

# 1) 看同一份数据、两次配置的对比（不用花 token：两条 run 已存在）
open http://localhost:5173/compare?left=112&right=100
#    → recall +27.5pp、p=2e-06、修好 19 题/变坏 0 题；点开翻转题看具体是哪些题

# 2) 打开报告，看"哪句答案在编"（run #155 已判定 60 题）
open http://localhost:5173/runs/155
#    → 报告卡：幻觉率 0.0% / 断言支持率 / 三率之和自洽校验
#    → 底部表格「主因」列 = flags[0]；点任意一题看抽屉：断言逐条 + 证据 + 检索上下文正文
#    → 「导出报告」下拉：CSV（含逐题明细）/ Markdown（贴群里）

# 3) 标注闭环：把某题标成 fixed，改配置重跑，验证是否真的修好
open http://localhost:5173/annotations?run=112   # 标 3 道 retrieval_miss 为 fixed
open http://localhost:5173/closure?baseline=112&candidate=100
#    → 3 题 improved（证据 recall 0% → 100%），可「一键验证」推进到 verified
#    → 点「改配置重跑」：从 #112 快照预填、只改 top_k 1→3，提交得新 run（#156 就是这么来的）

# 4) judge 校准（需要先有人工金标；30–50 题）
open http://localhost:5173/gold?run=155          # 1/2/3 判词 + 星级 + 复核标记
open http://localhost:5173/calibration?run=155   # κ / 混淆矩阵 / MAE / 判错清单

# 5) 可靠性：kill -9 之后还能续跑（不花 token）
make drill-compare REF=100   # 退出码 0 = 逐题与参照 run 一致
```

---

## 工程实践

**测试**：`295 passed / 20 skipped`（worker）+ Go 5 个包 + 前端 `285 passed`；live 层（`RUN_LIVE=1`）Go 真库 13 条、worker 2 条。一条命令跑完（不花 token）：`make regress`。分层策略：

| 层 | 手段 | 例子 |
|----|------|------|
| 纯函数单测 | 手算样例 + 边界 | κ 教科书样例 = 0.6；混淆矩阵逐格；`evidenceHighlights` 优先级 |
| 接口层 | httptest + 内存 stub | 权限三态（401/403）、状态机非法流转 409、跨项目 400 |
| **真库集成**（`RUN_LIVE=1`） | 真 PostgreSQL / Qdrant | 并发 `SKIP LOCKED` 不重复领取、checkpoint 事务、僵尸接管、跨项目不变量 |
| 端到端 | 无头 Chrome 量 DOM | 顶栏在 9 种宽度下是否截断；只读账号禁用按钮数（viewer 11 禁 7 / admin 0）；报告页 `var(--ev-*)` 是否真的生效（断言 `border-left` 计算值 = `rgb(24,160,88)`） |
| 演示即验收 | `scripts/demo.sh` | 从零建项目→传文档→跑评测→标注→对比→闭环，每一步打印预期 vs 实际 |

**故障演练**：60 题跑到一半 `kill -9` worker → 重启续跑，崩溃时已完成 4 条、续跑只处理 56 条，最终逐题结果与不中断跑一致（`make drill-compare`）。

**真实踩坑（都改成了测试）**：整行覆盖把分数/备注冲成 NULL（改成合并语义 + 守卫测试）、`SELECT` 加列忘改 `Scan`（列数不匹配只有真库测试能发现）、队列 live 测试误伤真实任务（加"独占队列"守卫）、worker 容器里 `PG_DSN` 变量名不对导致静默回落 localhost、镜像里宿主机 `0600` 文件权限导致非 root 读不了代码、**队列"少跑几条还报成功"**（30 题的 job 只跑了 20 条却是 `succeeded` —— 被强杀后条目停在 running 而领取只认 pending；现在续跑先归位、收尾还有未终结条目就以 failed 收尾并写清数字）、**建索引只有 CLI 入口**（同事在界面上传完文档就没有下一步，现在按 run 的快照配置自动建，见 D25）。

---

## 已知限制与 Roadmap

**限制**（诚实清单）

- 评测对象是**单轮 RAG 链路**；多轮对话、Agent 轨迹、工具调用指标未做。
- judge 仍是 LLM：协议与校准能暴露偏差，但不能保证"永远正确"——所以有了人工金标校准这一环，结论以 κ 与判错清单说话。
- 团队规模假设是"十来人、项目数个"：项目**全员可见**（D24），没有按人授权的成员表。
- 部署是单实例；多副本 worker 需要自己编排（数据侧已支持：`SKIP LOCKED` + 每 job 单持有）。
- **索引构建没有独立的界面入口**：提交评测时按需自动建（D25），想"先建好再跑"只能用 CLI（`index_corpus`）。做成"构建索引"按钮需要一个不属于任何 run 的任务类型，而 `jobs.run_id` 现在是 NOT NULL —— 要先动表结构，暂不做。
- 前端的图表是**进度条 + 标签 + 自绘色带**，没有引入图表库：当前要表达的是"比例/阈值/好坏"，不是趋势与分布；真要看分布曲线时再引（那时才需要一套完整配色与交互规范）。

**Roadmap**

- [x] M7-6：全量回归（`make regress`）+ 演示脚本（`scripts/demo.sh`）
- [~] M7-7：同事试用并修高优反馈 —— 试用工具包已就绪（[docs/trial.md](docs/trial.md)：20 分钟路径 + 反馈表 + 优先级规则），等排期
- [ ] 多轮 / Agent 轨迹指标（需要先定义轨迹数据模型）
- [ ] 检索侧接入 rerank 的对比维度（当前 `reranker.enabled` 已在快照里预留）
- [ ] 「要点覆盖」检测：用 `cases.reference_answer` 判"漏答要点"（等人工金标显示 judge 系统性漏判时再做）

---

## 目录结构

| 路径 | 内容 |
|------|------|
| `server/` | Go API：业务 CRUD、run 编排、报告聚合、A/B 计算、认证与 RBAC |
| `worker/` | Python 评测执行：检索 / 生成 / 判定 / 指标 / 归因 / CLI 工具 |
| `web/` | Vue3 + TS 控制台（报告、对比、标注、金标、校准、闭环、用户管理） |
| `datasets/` | 评测集资产（中文语料、jsonl 题目、标注脚本） |
| `docs/` | 部署手册、归因规则说明、可靠性设计、故障演练 |
| `scripts/` | 一键部署与备份、故障演练、并发演示 |
| `process.md` | **决策记录与里程碑（D1–D24）**：每个口径为什么这么定，都能在这里查到 |
| `Makefile` | 所有常用操作入口（`make help`） |
