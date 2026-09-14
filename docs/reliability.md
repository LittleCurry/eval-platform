# 可靠性设计（异步评测任务）

> 对应决策：D2（checkpoint）、D12（并发粒度）、D13（进度推送）、D7（可复现）。
> 实测证据与演练步骤见 `docs/fault-drills.md`；本文讲**为什么这么设计**与**边界在哪**。

## 1. 为什么必须异步

一次 60 题的检索评测 ≈ 60 次 embedding + 60 次向量检索 ≈ 20~40 秒；接入生成侧与 judge 后是分钟级。
同步 HTTP 会撞上三类问题：网关超时、客户端断开（浏览器关页面）、服务重启 = 全部白跑。
所以：**HTTP 只负责落库与入队（毫秒级返回），执行交给 worker**，并把"进度"变成数据库里的事实。

## 2. 状态机

| 对象 | 状态 | 谁推进 |
|---|---|---|
| `runs` | `pending → running → succeeded / failed` | 领取任务时置 running；`FinishJob` 时置终态（与 job 同事务） |
| `jobs` | `pending → running → succeeded / failed`（`paused` 用于 `--max-items` 暂停） | worker 领取 / 心跳 / 结束；被接管时回到 `pending` |
| `job_items` | `pending → running → succeeded / failed` | 逐条 checkpoint；死信即 `failed` |

`job_items` 的 `failed` 就是**死信（dead letter）**：重试超上限后不再重试，但**不阻塞**其余条目，
job 仍会跑完；最终 `run.status = failed`，`runs.error` 写明"N 条用例失败(已达重试上限)"。

## 3. checkpoint 的事务边界（整个设计的核心）

`CompleteJobItem` 在**一个事务**里做三件事：

1. `INSERT INTO case_results … ON CONFLICT (run_id, case_id) DO UPDATE`（结果落库）
2. `UPDATE job_items SET status='succeeded'`
3. `progressSQL`：从 `job_items` **实时聚合**出 `jobs.progress`，并顺手刷新 `heartbeat_at`

于是崩溃语义非常干净：

- 崩在事务**内** → 回滚：该条 case 仍是 `running`/`pending`，被接走后重跑；
- 崩在事务**间** → 已完成的部分是已提交事实，**不会重算**。

**执行语义 = at-least-once，结果语义 = exactly-once**：`case_results` 上有 `UNIQUE (run_id, case_id)`，
配合 `ON CONFLICT DO UPDATE`，即使某条 case 被重复执行（接管竞态、人工重跑），
结果也只是被**覆盖**而不会重复累加 —— 这就是为什么"指标不会被污染"。

> 另一个细节：`progress` **不是自增计数器**，而是每次从 `job_items` 重算。计数器会在崩溃时漂移，
> 重算永不漂移 —— 代价是每条 case 多一次聚合 `count(*)`（60 条规模可忽略）。

## 4. 心跳与僵尸任务接管

- worker 每 `--heartbeat-seconds`（默认 10s）调用一次心跳；`progressSQL` 也会顺带刷新心跳。
- **判定僵尸**：`status='running'` 且 `heartbeat_at` 早于 `now() - timeout`（API 默认 60s 判定用于前端提示）。
- **接管**：`reclaim_stale_jobs(timeout)`（worker 周期性调用 / `POST /api/v1/jobs/reclaim` 手动触发）
  1. 挑出心跳过期的 running job（`FOR UPDATE SKIP LOCKED`，**活跃任务不会被误接管**）；
  2. 它们的 `running` 微任务 → `pending`；
  3. job → `pending`，`error='心跳超时被接管(worker 可能被强杀)'`，并重算 progress。

接管后任何 worker 都能重新领取该 job，**只处理剩余 `pending` 条目**，这就是"断点续跑"。

## 5. 幂等边界与已知取舍（诚实记录）

| 项 | 现状 | 说明 / 触发条件 |
|---|---|---|
| 单 job 多 worker 分食 | **不支持**（D12） | 靠 `jobs.status` 单值守护；item 级租约是 backlog，触发条件：单 job 题量 > 500 或单 job 时长 > 10 min |
| `FailJobItem` 的事务性 | 两条语句非同一事务 | 第一条改 `job_items` 并提交，第二条重算 progress；因为 progress 是重算值，下次写入即自愈，故不影响正确性（严格一致化可包事务，属 backlog） |
| 外部 embedding 的非确定性 | 无法消除 | 同一 chunk 分数跨 run 中位差 ~3e-4、最大 ~2e-3 → **小于该量级的指标差异不可解读为提升**；对比实验要么多次重复，要么缓存 query embedding |
| worker 优雅停机（SIGTERM） | 未实现演练 | 见 `docs/fault-drills.md` 演练 2（待补） |

## 6. 两条运行路径（别混用）

| | 队列路径（`app.cli.worker`） | 离线对照路径（`app.cli.run_retrieval_eval`） |
|---|---|---|
| 定位 | **生产路径**：评测任务一律走这里 | **调试/对照**：改指标口径、试切分、离线复算时用 |
| 入队 | 由 `POST /api/v1/runs` 建 run + job + job_items | 不经队列，直接跑完写库 |
| 断点续跑 | ✅ checkpoint + 接管 | ❌ 中途失败即整次作废 |
| 进度 | `jobs.progress` 实时可见，前端进度条 | **没有 job** → `GET /runs/:id/progress` 返回 `job: null`（**有意行为**，前端显示"无队列任务"） |
| 并发 | 多 job 并行（D12） | 单进程串行 |
| 何时用 | 正式跑基线、出报告、给同事用 | 快速验证一个指标改动、复算 k 敏感性 |

> 两条路径都写同一张 `case_results` 表、同一套 `config_hash`，所以**结果可直接比对**（`make compare LEFT= RIGHT=`），
> 前提是配置一致 —— 这也是为什么 M2 时代用 CLI 跑出来的 run #3 至今仍能与队列跑的 run #22/#32/#99 逐题一致。

### 6.1 M4 起新增：生成配置即实验配置（D14）

生成评测的开关与参数**不来自 worker 的 CLI/环境变量**，而来自 run 的配置快照：

- 提交时带 `"generation": {...}` → 快照写入 `generation` 段 → worker 跑"检索 + 生成"；
- 提交时不带 → 快照**不含**该段 → worker 只跑检索（与 M2/M3 行为、指纹**字节级一致**）。

这样安排解决的是"报告里写的模型/prompt 与实际调用的不一致"这类不可复现问题：worker 一启动就读快照，参数不合法（prompt 不存在、段结构错、key 缺失）**在跑任何题之前**就让任务失败，而不是刷出一批死信。

`answer` 与 `generation` 元信息与检索结果写在**同一个 checkpoint 事务**里，因此续跑/接管后不会出现"有答案没指标"的半成品。

## 7. 三条可以直接讲的面试话术

1. **"进度不是查出来的，是记出来的。"**
   每次 case 完成都在同一事务里把结果、条目状态、进度汇总一起提交；HTTP 侧只读一行 JSON。
   因此进度条天然与"已完成的工作"一致，不存在"估计出来的百分比"。也正因为读一条记录就够，
   进度用 3s 短轮询即可（D13），WebSocket 属于"为了实时日志面板"才值得引入的复杂度。

2. **"worker 被 kill -9 之后，系统自己把活捡回来。"**
   心跳 + 阈值判定僵尸任务 + `SKIP LOCKED` 接管 + `case_results` 唯一键幂等 —— 实测 60 题跑一半强杀，
   崩溃时已完成 4 条，重启后 `processed=56`（恰好等于剩余），最终指标与不中断跑**逐题一致**。

3. **"可复现不等于'再跑一次数字一样'。"**
   可复现靠四件套（`git_sha` + `config_snapshot`/`config_hash` + 数据集/语料 id + 模型版本）：
   同一 `config_hash` 跨 4 个代码版本的 4 次 run，`recall@k` 逐位相同；
   但**单题排名的分数有 1e-3 量级抖动**（外部 embedding 服务非确定性），
   所以对比实验的判定门槛必须建立在噪声底之上 —— 这也是用轮询/单次跑得出的"提升"需要谨慎的原因。
