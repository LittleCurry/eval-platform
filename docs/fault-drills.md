# 故障演练手册（Fault Drills）

> 目标：证明"批量评测任务中断后能续跑"不是口头承诺，而是可复现的工程能力。
> 面试时可直接跑 `scripts/fault_drill.sh` 演示。

## 演练 1：worker 被强杀（kill -9）后的接管与续跑

### 前置

```bash
make up-deps && make migrate-up     # PG(healthy) + Qdrant
make api &                          # API 服务(需包含 POST /api/v1/jobs/reclaim)
# 语料索引已构建(collection corpus4_5f45e034)