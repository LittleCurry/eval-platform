# 故障演练手册（Fault Drills）

> 目标：证明"批量评测任务中断后能续跑"不是口头承诺，而是可复现的工程能力。
> 面试时可直接跑 `scripts/fault_drill.sh` 演示。

**一条命令**：

```bash
make up-deps && make migrate-up && make api    # 依赖 + 迁移 + API(reclaim/进度接口)
make drill                                     # 演练(v2 60 题), 保留本次 run 作为证据
make drill-compare REF=100                      # 演练 + 与 run 100 逐题比对(验收闸门)
make compare LEFT=100 RIGHT=101                 # 只做逐题比对
```

---

## 演练 1：worker 被强杀（kill -9）后的接管与续跑

### 前置

- `make up-deps`（PG healthy + Qdrant）→ `make migrate-up` → `make api`
- 语料索引已构建：collection `corpus4_5f45e034`（21 篇 → 80 chunks）
- 评测集已导入：dataset 4（v2，60 题）/ dataset 3（v1，30 题）
- worker venv 可用；`.env` 里有 `SILICONFLOW_API_KEY`

### 执行

```bash
# 默认: dataset 4(60 题), 慢速小批量便于中途强杀
scripts/fault_drill.sh

# 关键旋钮
DATASET_ID=3 scripts/fault_drill.sh                        # 30 题快版
KILL_AFTER=15 BATCH_SIZE=1 BATCH_PAUSE_MS=2000 \
  COMPARE_WITH=100 scripts/fault_drill.sh                  # 调大崩溃窗口 + 自动比对
STRICT=1 COMPARE_WITH=100 scripts/fault_drill.sh           # 连命中序列也严格判定(见"判定口径")
CLEANUP_RUN=1 scripts/fault_drill.sh                       # 演练后删除本次 run
```

脚本默认 **保留**本次 run —— 它是"续跑结果与全量跑一致"这条验收的右操作数，删了就无从对比。

### 预期观测（每一步该看到什么）

| 步骤 | 观测点 | 期望 |
|---|---|---|
| 0 | `GET /healthz` | 200；否则脚本立刻退出（**不要**在没有 API 时跑演练：`curl -s` 会静默失败，看起来像"什么都没发生"） |
| 1 | 提交任务返回的 `items` | 必须等于该数据集的用例数（脚本自动推导并硬校验） |
| 3 | `kill -9` 后的进度 | `succeeded` 至少 1（否则本次未覆盖 checkpoint 场景，脚本会给出提示） |
| 4 | `POST /jobs/reclaim` | `jobs >= 1`（僵尸任务被收回）；`items` 是崩溃瞬间"在跑"的微任务数 |
| 5 | 续跑输出的 `processed` | **必须 ≤ 崩溃时的剩余条数**（超过即说明重算了已完成用例，脚本直接判失败） |
| 6 | run 状态 / 进度 | `succeeded`，`pending=running=0`，`succeeded=总题数` |
| 7 | `compare_runs` 退出码 | 0 = 与参照 run 逐题一致 |

### 判定口径（compare_runs 的两层口径）

- **判定口径**（影响退出码）：run 级 metrics、单题 metrics（recall / precision / mrr / first_hit_rank /
  gold_count / hits）、归因标签、题目集合。
- **参考口径**（不影响退出码）：top-k 命中序列与分数。
  原因：query embedding 由外部服务产生，**不是逐位确定性的** —— 实测同一 `(qid, point_id)` 的分数
  跨 run 中位差 **3.1e-4 ~ 4.4e-4**、最大 **2.1e-3**（仅 3.3% 完全相同）。近似平局的候选会因此换位，
  但 gold 的召回与位次不变。需要更严时可加 `--strict`。
- **分数噪声会被打印**：任何小于该量级的指标差异都不该被解读为"提升/退化"（M5 做 A/B 时的判定门槛）。

### 实测记录

**2026-09-11，v2 60 题（dataset 4，`config_hash=4e767020`）**

| 项 | 值 |
|---|---|
| 参照 | run **100**（不中断全量跑完，job 89） |
| 演练 | run **101**（job 90），`BATCH_SIZE=1 BATCH_PAUSE_MS=1500 KILL_AFTER=12` |
| 崩溃现场 | `pending=56 succeeded=4 failed=0 total=60`（崩溃点落在批次边界，故没有 in-flight 的 running item） |
| 接管结果 | `jobs=1, items=0`（回收了任务本身；in-flight 微任务数为 0，与崩溃点相符） |
| 续跑 | `processed=56`，恰好等于崩溃时剩余 56 条 → **已完成 4 条未重算** |
| 最终 | `succeeded=60/60`，`recall@5=0.948611`、`mrr@5=0.883889`、`hit@5=1.0` |
| 逐题比对 | 判定口径 **60/60 一致**（退出码 0）；命中序列 2 题不同（zjc-027、zjc-050）；分数噪声 299 对，中位 3.09e-4、最大 2.13e-3 |

### 生产参数建议

| 参数 | 演练值 | 生产值 |
|---|---|---|
| 心跳间隔 `--heartbeat-seconds` | 10s | 10s |
| 接管阈值 `older_than_seconds` | **0**（演练要立刻看到效果） | **60s+**，或由 worker 的周期性 `reclaim_stale()` 自动接管 |
| 批量 `--batch-size` | 1（拉长崩溃窗口） | 8~16 |
| 批次间暂停 `--batch-pause-ms` | 1500 | 0 |
| 单条重试上限 `--max-retries` | 3 | 3（超过进 dead letter，不吞错） |

### 已知边界（诚实记录）

1. **单 job 只能被一个 worker 消费**（`jobs.status` 单值守护）：多 worker 的并发粒度是 **job 级**，
   不是 item 级。要做"多 worker 分食同一 job"需要给 `job_items` 加租约（`worker_id` + 过期时间），
   触发条件：单 job 题量 > 500 或单 job 时长 > 10 min。
2. **本脚本的崩溃点常落在批次边界**，此时没有 in-flight 微任务，接管只会回收任务本身
   （`items=0`）。要覆盖"释放 in-flight 微任务"的分支，需让崩溃发生在批次中间：
   调大 `BATCH_SIZE`（如 8）并调小 `BATCH_PAUSE_MS`，或把 `KILL_AFTER` 设在某个批次处理中。
3. **调试一律用 `--job-id`，不要用 `--once`**：`--once` 领取的是"最新可领任务"，会把前端/脚本
   顺手提交的 pending run 一起跑掉（本项目已因此把一条 30 题的旧 run 误当成 60 题回归跑完）。
4. **序列不一致不等于结果错误**（见"判定口径"）：只有 gold 相关判定变化才是真问题。

---

## 演练 2：优雅停机（SIGTERM）—— 待补

计划：向 worker 发 SIGTERM，验证当前批次跑完后进程退出、未完成条目回到 `pending`、
重启后从断点继续（与 kill -9 的差别是**不需要等心跳超时**）。本演练**尚未执行**，故不在此写期望值。
