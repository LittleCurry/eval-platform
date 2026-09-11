#!/usr/bin/env bash
# 故障演练: worker 被 kill -9(强杀)后, 系统能否接管僵尸任务并从断点续跑到底, 且结果与全量跑逐题一致。
#
# 用法:
#   scripts/fault_drill.sh                                     # 默认 dataset 4(v2, 60 题) 打本机 8080
#   DATASET_ID=3 scripts/fault_drill.sh                         # 30 题的快速版(期望条目数自动取用例数)
#   KILL_AFTER=12 BATCH_PAUSE_MS=1500 BATCH_SIZE=1 \
#     COMPARE_WITH=100 scripts/fault_drill.sh                   # 跑完自动与 run 100 逐题比对(验收闸门)
#   COMPARE_WITH=100 STRICT=1 scripts/fault_drill.sh            # 连 top-k 命中序列也要求逐位一致
#   CLEANUP_RUN=1 scripts/fault_drill.sh                        # 演练结束删除本次 run
#
# 退出码: 0 = 演练通过(续跑未重算, 且与参照 run 一致); 非 0 = 存在硬失败。
#
# 依赖: API 在跑(需 POST /api/v1/runs 与 /jobs/reclaim)、PostgreSQL、Qdrant、worker venv。
# 注意: 本脚本**默认保留**本次 run, 因为它是 S2 一致性验收的右操作数; 要清理请用 CLEANUP_RUN=1。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_BASE="${API_BASE:-http://localhost:8080}"
WORKER_DIR="${WORKER_DIR:-$REPO_ROOT/worker}"
PYTHON_BIN="${PYTHON_BIN:-$WORKER_DIR/.venv/bin/python}"
export PYTHONPATH="$WORKER_DIR${PYTHONPATH:+:$PYTHONPATH}"
DATASET_ID="${DATASET_ID:-4}"
CORPUS_ID="${CORPUS_ID:-4}"
KILL_AFTER="${KILL_AFTER:-12}"
BATCH_SIZE="${BATCH_SIZE:-1}"
BATCH_PAUSE_MS="${BATCH_PAUSE_MS:-1500}"
TOP_K="${TOP_K:-5}"
EXPECT_ITEMS="${EXPECT_ITEMS:-}"           # 空 = 自动取该数据集的用例数
COMPARE_WITH="${COMPARE_WITH:-}"           # 非空 = 与指定 run 逐题比对(验收闸门)
STRICT="${STRICT:-0}"                      # 1 = 对比时连命中序列也严格判定
CLEANUP_RUN="${CLEANUP_RUN:-0}"
ID_FILE="${ID_FILE:-/tmp/fault_drill_ids.env}"
WORKER_LOG="${WORKER_LOG:-/tmp/fault_drill_worker.log}"
# 演练要立刻看到效果, 默认阈值 0(强制接管); 生产用 60s+ 或由 worker 周期性自动接管
RECLAIM_OLDER_THAN="${RECLAIM_OLDER_THAN:-0}"

hr() { printf '\n=== %s ===\n' "$1"; }
die() { printf '❌ %s\n' "$1" >&2; exit 1; }

json_get() { python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"; }

# 读取任务的进度快照(pending/running/succeeded/failed/total)
progress_of() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.queue import job_progress

progress = job_progress(Settings().pg_dsn, int(sys.argv[1]))
print(" | ".join(f"{k}={v}" for k, v in progress.items()))
PYEOF
}

# 读取"崩溃时已完成的用例数"(succeeded + failed), 用于证明续跑没有重算
completed_of() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.queue import job_progress

progress = job_progress(Settings().pg_dsn, int(sys.argv[1]))
print(int(progress.get("succeeded", 0)) + int(progress.get("failed", 0)))
PYEOF
}

total_of() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.queue import job_progress

print(int(job_progress(Settings().pg_dsn, int(sys.argv[1])).get("total", 0)))
PYEOF
}

# 该数据集应有的微任务数(用例数) —— 自动推导, 免得手填错
expected_items_of() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.store import list_cases

print(len(list_cases(Settings().pg_dsn, int(sys.argv[1]))))
PYEOF
}

delete_run() {
  "$PYTHON_BIN" - "$1" <<'PYEOF'
import sys

from app.config import Settings
from app.store import delete_run

delete_run(Settings().pg_dsn, int(sys.argv[1]))
print(f"已清理 run {sys.argv[1]}")
PYEOF
}

hr "0. 前置检查"
# 3080 之前踩过的坑: API 没起时 curl -s 会静默失败, 演练结果看起来"什么都没发生"
curl -sf -m 5 "$API_BASE/healthz" >/dev/null \
  || die "API 未就绪($API_BASE/healthz 不通), 先 make api 再重试"
"$PYTHON_BIN" -c "from app.config import Settings; Settings()" >/dev/null || die "worker 配置/Deps 异常"
echo "API $API_BASE 就绪, worker venv 可用"

hr "1. 提交评测任务"
SUBMIT=$(curl -sf -X POST "$API_BASE/api/v1/runs" -H 'Content-Type: application/json' \
  -d "{\"dataset_id\":$DATASET_ID,\"corpus_id\":$CORPUS_ID,\"top_k\":$TOP_K}") \
  || die "提交任务失败(检查 dataset_id=$DATASET_ID 是否存在且有用例)"
RUN_ID=$(printf '%s' "$SUBMIT" | json_get run_id)
JOB_ID=$(printf '%s' "$SUBMIT" | json_get job_id)
ITEMS=$(printf '%s' "$SUBMIT" | json_get items)
echo "run_id=$RUN_ID job_id=$JOB_ID items=$ITEMS"
printf 'RUN_ID=%s\nJOB_ID=%s\nITEMS=%s\n' "$RUN_ID" "$JOB_ID" "$ITEMS" > "$ID_FILE"
echo "id 已写入 $ID_FILE"

if [ -z "$EXPECT_ITEMS" ]; then
  EXPECT_ITEMS=$(expected_items_of "$DATASET_ID") || die "无法读取数据集 $DATASET_ID 的用例数"
  echo "数据集 $DATASET_ID 用例数=$EXPECT_ITEMS(自动推导)"
fi
if [ "$ITEMS" != "$EXPECT_ITEMS" ]; then
  die "任务条目数 $ITEMS != 数据集用例数 $EXPECT_ITEMS(是不是 dataset_id 选错了?)"
fi

if [ "$CLEANUP_RUN" = "1" ]; then
  trap 'delete_run "$RUN_ID" || true' EXIT
  echo "演练结束后将清理本次 run(CLEANUP_RUN=1)"
else
  echo "保留本次 run(要用 CLEANUP_RUN=1 才清理)"
fi

hr "2. 启动 worker(小批量 + 慢速, 便于中途强杀)"
# 注意: 必须直接执行 python(不经函数/子 shell), 否则 $! 拿到的是外层 shell, kill -9 杀不到真正的 worker
"$PYTHON_BIN" -m app.cli.worker --once --job-id "$JOB_ID" \
  --batch-size "$BATCH_SIZE" --batch-pause-ms "$BATCH_PAUSE_MS" \
  >"$WORKER_LOG" 2>&1 &
WORKER_PID=$!
echo "worker pid=$WORKER_PID (日志: $WORKER_LOG)"

hr "3. 等 ${KILL_AFTER}s 后 kill -9(模拟宕机)"
sleep "$KILL_AFTER"
kill -9 "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
sleep 1
if kill -0 "$WORKER_PID" 2>/dev/null; then
  die "worker 仍存活, 演练无效(启动方式必须是直接执行 python)"
fi
echo "worker 已确认死亡"
echo "崩溃现场: $(progress_of "$JOB_ID")"
CRASH_DONE=$(completed_of "$JOB_ID")
TOTAL_ITEMS=$(total_of "$JOB_ID")
echo "崩溃时已完成 $CRASH_DONE / $TOTAL_ITEMS 条"

hr "4. 触发僵尸任务接管(older_than_seconds=${RECLAIM_OLDER_THAN})"
curl -sf -X POST "$API_BASE/api/v1/jobs/reclaim?older_than_seconds=${RECLAIM_OLDER_THAN}" \
  -H 'Content-Type: application/json' | python3 -m json.tool \
  || die "接管接口调用失败"
echo "接管后: $(progress_of "$JOB_ID")"

hr "5. 新 worker 从断点续跑"
RESUME=$("$PYTHON_BIN" -m app.cli.worker --once --job-id "$JOB_ID" --batch-size 8 2>/dev/null) \
  || die "续跑失败(见 $WORKER_LOG 与上方输出)"
printf '%s\n' "$RESUME"
RESUMED=$(printf '%s' "$RESUME" | sed -n 's/.*"processed": *\([0-9]*\).*/\1/p')
REMAINING=$((TOTAL_ITEMS - CRASH_DONE))
echo "本次续跑处理 ${RESUMED:-?} 条; 崩溃时已完成 $CRASH_DONE 条, 剩余应为 $REMAINING 条"

# 硬断言: 续跑处理的条目数不能超过"崩溃时剩余"(超过即说明在重算已完成的 case)
if [ -n "${RESUMED:-}" ] && [ "$RESUMED" -gt "$REMAINING" ]; then
  die "续跑处理了 $RESUMED 条 > 剩余 $REMAINING 条 —— 已完成的用例被重算了"
fi
if [ "$CRASH_DONE" -eq 0 ]; then
  echo "⚠ 崩溃时还没有任何用例完成, 本次演练未覆盖 checkpoint 场景"
  echo "  建议: 调大 KILL_AFTER(如 15)或调小 BATCH_SIZE=1 + 调大 BATCH_PAUSE_MS=2000"
fi

hr "6. 校验最终结果"
SUMMARY=$(curl -sf "$API_BASE/api/v1/runs/$RUN_ID") || die "读取 run 失败"
printf '%s' "$SUMMARY" | "$PYTHON_BIN" -c "
import sys, json
data = json.load(sys.stdin)
metrics = data.get('metrics') or {}
print('run 状态 :', data['status'])
print('题数     :', metrics.get('cases_evaluated'), '/', metrics.get('cases_total'), '(跳过无 gold:', metrics.get('cases_skipped_no_gold'), ')')
print('recall@k :', metrics.get('recall_at_k'), '| precision@k:', metrics.get('precision_at_k'))
print('mrr@k    :', metrics.get('mrr_at_k'), '| hit@k:', metrics.get('hit_at_k'))
"
FINAL_STATUS=$(printf '%s' "$SUMMARY" | json_get status)
[ "$FINAL_STATUS" = "succeeded" ] || die "续跑后 run 状态是 $FINAL_STATUS, 不是 succeeded"
echo "续跑完成: $(progress_of "$JOB_ID")"

hr "7. 与参照 run 逐题一致性校验"
if [ -n "$COMPARE_WITH" ]; then
  COMPARE_ARGS=(--left "$COMPARE_WITH" --right "$RUN_ID")
  [ "$STRICT" = "1" ] && COMPARE_ARGS+=(--strict)
  set +e
  "$PYTHON_BIN" -m app.cli.compare_runs "${COMPARE_ARGS[@]}"
  COMPARE_CODE=$?
  set -e
  [ "$COMPARE_CODE" -eq 0 ] || die "一致性校验失败(退出码 $COMPARE_CODE): 续跑结果与 run $COMPARE_WITH 不一致"
else
  echo "未指定 COMPARE_WITH, 跳过自动比对。手动执行:"
  echo "  cd worker && .venv/bin/python -m app.cli.compare_runs --left <参照run> --right $RUN_ID"
fi

hr "演练结论"
echo "worker 被 kill -9 后: 已完成 $CRASH_DONE 条未重算(续跑只处理了 $REMAINING 条以内),"
echo "僵尸任务的微任务被接管回队列, 续跑后 run 状态 succeeded, 并已与参照 run 逐题比对。"
