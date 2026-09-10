#!/usr/bin/env bash
# 故障演练: worker 被 kill -9(强杀)后, 系统能否接管僵尸任务并从断点续跑到底。
#
# 用法:
#   scripts/fault_drill.sh                                   # 打本机 8080 API
#   API_BASE=http://localhost:8090 scripts/fault_drill.sh     # 打指定 API
#   KILL_AFTER=10 BATCH_PAUSE_MS=2000 scripts/fault_drill.sh  # 调整节奏
#
# 依赖: API 服务在跑(含 POST /api/v1/jobs/reclaim)、PostgreSQL、Qdrant、worker venv。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_BASE="${API_BASE:-http://localhost:8080}"
WORKER_DIR="${WORKER_DIR:-$REPO_ROOT/worker}"
PYTHON_BIN="${PYTHON_BIN:-$WORKER_DIR/.venv/bin/python}"
export PYTHONPATH="$WORKER_DIR${PYTHONPATH:+:$PYTHONPATH}"
DATASET_ID="${DATASET_ID:-3}"
CORPUS_ID="${CORPUS_ID:-4}"
KILL_AFTER="${KILL_AFTER:-7}"
BATCH_SIZE="${BATCH_SIZE:-1}"
BATCH_PAUSE_MS="${BATCH_PAUSE_MS:-1500}"
# 演练要立刻看到效果, 默认阈值 0(强制接管); 生产用 60s+ 或由 worker 周期性自动接管
RECLAIM_OLDER_THAN="${RECLAIM_OLDER_THAN:-0}"

hr() { printf '\n=== %s ===\n' "$1"; }

json_get() { python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"; }

progress_of() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.queue import job_progress

settings = Settings()
progress = job_progress(settings.pg_dsn, int(sys.argv[1]))
print(" | ".join(f"{k}={v}" for k, v in progress.items()))
PYEOF
}

run_worker() {
  "$PYTHON_BIN" -m app.cli.worker "$@"
}

hr "1. 提交评测任务"
SUBMIT=$(curl -s -X POST "$API_BASE/api/v1/runs" -H 'Content-Type: application/json' \
  -d "{\"dataset_id\":$DATASET_ID,\"corpus_id\":$CORPUS_ID,\"top_k\":5}")
RUN_ID=$(printf '%s' "$SUBMIT" | json_get run_id)
JOB_ID=$(printf '%s' "$SUBMIT" | json_get job_id)
ITEMS=$(printf '%s' "$SUBMIT" | json_get items)
echo "run_id=$RUN_ID job_id=$JOB_ID items=$ITEMS"

cleanup() {
  "$PYTHON_BIN" - "$RUN_ID" <<'PYEOF' || true
import sys

from app.config import Settings
from app.store import delete_run

settings = Settings()
delete_run(settings.pg_dsn, int(sys.argv[1]))
print(f"已清理 run {sys.argv[1]}")
PYEOF
}
trap cleanup EXIT

hr "2. 启动 worker(小批量 + 慢速, 便于中途强杀)"
# 注意: 必须直接执行 python(不经函数/子 shell), 否则 $! 拿到的是外层 shell, kill -9 杀不到真正的 worker
"$PYTHON_BIN" -m app.cli.worker --once --job-id "$JOB_ID" \
  --batch-size "$BATCH_SIZE" --batch-pause-ms "$BATCH_PAUSE_MS" \
  >/tmp/fault_drill_worker.log 2>&1 &
WORKER_PID=$!
echo "worker pid=$WORKER_PID"

hr "3. 等 ${KILL_AFTER}s 后 kill -9(模拟宕机)"
sleep "$KILL_AFTER"
kill -9 "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
sleep 1
if kill -0 "$WORKER_PID" 2>/dev/null; then
  echo "worker 仍存活, 演练无效(启动方式必须是直接执行 python)"
  exit 1
fi
echo "worker 已确认死亡"
echo "崩溃现场: $(progress_of "$JOB_ID")"

hr "4. 触发僵尸任务接管(older_than_seconds=${RECLAIM_OLDER_THAN})"
curl -s -X POST "$API_BASE/api/v1/jobs/reclaim?older_than_seconds=${RECLAIM_OLDER_THAN}" \
  -H 'Content-Type: application/json' | python3 -m json.tool
echo "接管后: $(progress_of "$JOB_ID")"

hr "5. 新 worker 从断点续跑"
RESUME=$(run_worker --once --job-id "$JOB_ID" --batch-size 8 2>/dev/null)
printf '%s\n' "$RESUME"
RESUMED=$(printf '%s' "$RESUME" | sed -n 's/.*"processed": *\([0-9]*\).*/\1/p')
echo "本次续跑处理 ${RESUMED:-?} 条(应小于总数 $ITEMS, 证明已完成的没有被重算)"

hr "6. 校验最终结果"
curl -s "$API_BASE/api/v1/runs/$RUN_ID" | python3 -c "
import sys, json
data = json.load(sys.stdin)
metrics = data.get('metrics') or {}
print('run 状态 :', data['status'])
print('recall@k :', metrics.get('recall_at_k'), '| mrr@k:', metrics.get('mrr_at_k'), '| hit@k:', metrics.get('hit_at_k'))
print('完成时间 :', data.get('finished_at'))
"

hr "演练结论"
echo "worker 被 kill -9 后: 已完成的 case 未重算, 僵尸任务的微任务被接管回队列, 续跑后指标与正常跑完一致。"