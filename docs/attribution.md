# 归因规则（M4-3）

> 报告要回答的问题不是"幻觉率多少"，而是**"这批坏样本，责任在检索、生成还是数据"**。
> 本文是这套规则的唯一说明书：谓词、优先级、阈值、降级矩阵、回放用法。
> 改规则前请读完 §7 的四步清单 —— 规则一改，历史 run 的标签含义就变了。

## 1. 9 个标签

| 优先级 | 标签 | 精确谓词 | 依赖信号 | 语义 |
|---|---|---|---|---|
| 1 | `no_gold` | `gold_count == 0` | 检索 | 数据集缺 gold，检索侧不可评测 |
| 2 | `anchor_incomplete` | `hit == 0` 且判定开启 且 `claims` 非空 且 unsupported=0 且 irrelevant=0 | 检索+判定 | 没召回 gold 但答案全有据 → **锚点漏标**（数据问题） |
| 3 | `retrieval_miss` | `hit == 0` | 检索 | gold 全未召回 |
| 4 | `retrieval_partial` | `hits < gold_count`（多 gold 题） | 检索 | 部分 gold 未召回（数量随 k 变化） |
| 5 | `retrieval_low_rank` | `first_hit_rank > ceil(k × 0.5)` | 检索 | 证据在但排位靠后（k=5 → 排位>3） |
| 6 | `hallucination` | `unsupported > 0` | 判定 | 有断言无证据 |
| 7 | `off_topic` | `irrelevant > 0` | 判定 | 有据但答非所问 |
| 8 | `generation_quality` | `hit==1` 且 unsupported=0 且 irrelevant=0 且 rubric 存在 且 `min(relevance, helpfulness) ≤ quality_line` | 检索+判定 | 检索到位、无幻觉，质量仍不达标 |
| 9 | `no_claims` | 判定开启 且 `claims` 为空 | 判定 | 无可核查断言（信息性） |

`no_gold` 与 `no_claims` 是 M3/M4-1 起就已在库的既有标签，名字不能改（报告筛选用得到）。

## 2. 顺序即主因（D16）

`case_results.flags` 是**按优先级排好序**的数组：**`flags[0]` 就是主因**，不新增 `primary_flag` 列。
因此任何读取方（Go 报告、CLI、前端）都能零成本推导主因，也不必在规则升级后回填历史数据。

两条容易被问的排序：

- **`anchor_incomplete` 压过 `retrieval_miss`**：同样是 `hit == 0`，但断言全部有据 → 证据其实就在被召回的 chunk 里，说明是**数据集锚点漏标**。主因归数据，否则你会去优化检索，而真正该修的是评测集。
- **`generation_quality` 的前置是无幻觉**：`unsupported > 0` 时不再叠加质量标签。否则一个编造的坏答案会在"幻觉"和"质量"两栏**各计一次**，让分布虚高。
- 同一条链路上多个标签会同时出现（如 `retrieval_miss` + `hallucination`），报告可按需展开；主因只看第一位。

## 3. 事实 ≠ 责任（为什么用"环节缺陷"而不是"错误归因"）

run #155 的实测：`retrieval_partial` 命中 5 题（zjc-017/023/024/026/031），而这 5 题**都没有导致错答**
（144 条断言全部 supported）。它描述的是"检索没做全"（潜在风险），不是"这题答错了"。

所以报告分两层用：

- **L1 环节缺陷分布**：`flag_counts`（每类标签各多少条）；
- **L2 坏样本主因**：`flags[0]` 的分布（只统计真正不达标的题）。

## 4. 降级矩阵：没有判定 ≠ 没有幻觉

| run 类型 | 可用信号 | 可出标签 | `metrics.attribution.scope` |
|---|---|---|---|
| 只跑检索（如 run #100/#112） | 单题检索指标 | 1–5 | `retrieval` |
| 检索 + 生成（判定未启用） | 指标 + 答案 | 1–5 | `retrieval` |
| 检索 + 生成 + 判定（如 run #155） | 全部 | 1–9 | `retrieval+judge` |

判定结论缺失时（`case_results.judge` 是 `{}`）**一个判定类标签都不出**；`rubric` 缺失时不出 `generation_quality`
（**"没打分"绝不能当成"合格"**）。这条规矩来自 M4-2 的教训：rubric v1 给完全编造的答案打了 `relevance=5 / helpfulness=5`。

## 5. 阈值必须随 run 落库（D15）

`runs.metrics.attribution` = `{version, scope, k, low_rank_limit, low_rank_ratio, quality_line}`。

**为什么不进 `config_snapshot`**：快照参与 `config_hash`，加一段会让**全部历史 run 的指纹作废**，
M2 以来"同 `config_hash` 跨代码版本指标逐位一致"的复现证据与 `compare_runs` 的同一配置判定会一起断掉。
改口径是**重新解释既有事实**，不是换配置。

**为什么必须记**：标签数依赖 k 与阈值。实测同一批 60 题：

| run | k | `low_rank_limit` | 标签分布 |
|---|---|---|---|
| #112 | 1 | 1（**永不触发**） | `retrieval_miss 13 + retrieval_partial 12` |
| #100 | 5 | 3 | `retrieval_partial 5 + retrieval_low_rank 1` |
| #155 | 5 | 3 | 同 #100，另 `scope=retrieval+judge` |

同一套规则、同一批题，只因 k 不同，`retrieval_partial` 就从 12 条变成 5 条 —— 不记 k 就没法跨 run 比。

`quality_line` 默认 3（保守）：run #155 在 3 下出 **0** 条质量标签，放到 4 立刻变 **10** 条。
**阈值校准留给 M6 的人工金标**，M4-3 只保证"用哪个值就记哪个值"。

## 6. 重算 CLI：规则升级不重跑评测

```bash
cd worker
.venv/bin/python -m app.cli.attribution --run-id 112                    # dry-run（默认）: 分布 + 差异明细
.venv/bin/python -m app.cli.attribution --run-id 112 --apply            # 写库: flags + metrics.attribution
.venv/bin/python -m app.cli.attribution --run-id 155 --quality-line 4   # 换达标线试算
.venv/bin/python -m app.cli.attribution --run-id 155 --json             # 机器可读