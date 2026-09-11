#!/usr/bin/env bash
# 并发粒度演示(D12): **两个 job 各被一个 worker 领取, 并行跑完, 互不阻塞**。
#
# 这个脚本同时产出 M5 需要的第一组 A/B 数据: 同一数据集上 top_k=5 与 top_k=1 的对照。
#
# 用法:
#   scripts/parallel_jobs_demo.sh                          # dataset 4(v2 60 题), top_k=5 vs top_k=1
#   TOP_K_A=5 TOP_K_B=3 scripts/parallel_jobs_demo.sh      # 换对照参数
#   CLEANUP_RUN=1 scripts/parallel_jobs_demo.sh            # 跑完删除两个 run
#
# 退出码: 0 = 两个 job 确实并行执行且都成功; 非 0 = 断言失败。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_BASE="${API_BASE:-http://localhost:8080}"
WORKER_DIR="${WORKER_DIR:-$REPO_ROOT/worker}"
PYTHON_BIN="${PYTHON_BIN:-$WORKER_DIR/.venv/bin/python}"
export PYTHONPATH="$WORKER_DIR${PYTHONPATH:+:$PYTHONPATH}"
DATASET_ID="${DATASET_ID:-4}"
CORPUS_ID="${CORPUS_ID:-4}"
TOP_K_A="${TOP_K_A:-5}"
TOP_K_B="${TOP_K_B:-1}"
BATCH_SIZE="${BATCH_SIZE:-8}"
SAMPLE_INTERVAL="${SAMPLE_INTERVAL:-0.5}"
CLEANUP_RUN="${CLEANUP_RUN:-0}"

hr() { printf '\n=== %s ===\n' "$1"; }
die() { printf '❌ %s\n' "$1" >&2; exit 1; }
json_get() { python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"; }

submit_run() {  # $1 = top_k
  curl -sf -X POST "$API_BASE/api/v1/runs" -H 'Content-Type: application/json' \
    -d "{\"dataset_id\":$DATASET_ID,\"corpus_id\":$CORPUS_ID,\"top_k\":$1}"
}

run_status() {
  curl -sf "$API_BASE/api/v1/runs/$1/progress" | json_get status
}

cleanup() {
  for rid in "${RUN_A:-}" "${RUN_B:-}"; do
    [ -n "$rid" ] || continue
    "$PYTHON_BIN" - "$rid" <<'PYEOF' || true
import sys

from app.config import Settings
from app.store import delete_run

delete_run(Settings().pg_dsn, int(sys.argv[1]))
print(f"已清理 run {sys.argv[1]}")
PYEOF
  done
}

hr "0. 前置检查"
curl -sf -m 5 "$API_BASE/healthz" >/dev/null || die "API 未就绪($API_BASE/healthz 不通), 先 make api"

hr "1. 提交两个任务(同数据集, 不同 top_k)"
SUBMIT_A=$(submit_run "$TOP_K_A") || die "提交任务 A 失败"
SUBMIT_B=$(submit_run "$TOP_K_B") || die "提交任务 B 失败"
RUN_A=$(printf '%s' "$SUBMIT_A" | json_get run_id); JOB_A=$(printf '%s' "$SUBMIT_A" | json_get job_id)
RUN_B=$(printf '%s' "$SUBMIT_B" | json_get run_id); JOB_B=$(printf '%s' "$SUBMIT_B" | json_get job_id)
ITEMS_A=$(printf '%s' "$SUBMIT_A" | json_get items); ITEMS_B=$(printf '%s' "$SUBMIT_B" | json_get items)
echo "A: run=$RUN_A job=$JOB_A top_k=$TOP_K_A items=$ITEMS_A"
echo "B: run=$RUN_B job=$JOB_B top_k=$TOP_K_B items=$ITEMS_B"
[ "$ITEMS_A" = "$ITEMS_B" ] || die "两个任务的条目数不一致($ITEMS_A vs $ITEMS_B)"
[ "$RUN_A" != "$RUN_B" ] || die "两次提交拿到了同一个 run"

if [ "$CLEANUP_RUN" = "1" ]; then
  trap cleanup EXIT
  echo "结束后清理两个 run(CLEANUP_RUN=1)"
fi

hr "2. 同时启动两个 worker 进程(各占一个 job)"
"$PYTHON_BIN" -m app.cli.worker --once --job-id "$JOB_A" --batch-size "$BATCH_SIZE" \
  >/tmp/parallel_demo_a.log 2>&1 &
PID_A=$!
"$PYTHON_BIN" -m app.cli.worker --once --job-id "$JOB_B" --batch-size "$BATCH_SIZE" \
  >/tmp/parallel_demo_b.log 2>&1 &
PID_B=$!
echo "worker A pid=$PID_A (job $JOB_A) | worker B pid=$PID_B (job $JOB_B)"

hr "3. 采样: 两个任务是否真的同时在跑"
SAMPLES=0
BOTH_RUNNING=0
FIRST_BOTH=""
while kill -0 "$PID_A" 2>/dev/null || kill -0 "$PID_B" 2>/dev/null; do
  SA=$(run_status "$RUN_A" 2>/dev/null || echo "?")
  SB=$(run_status "$RUN_B" 2>/dev/null || echo "?")
  SAMPLES=$((SAMPLES + 1))
  if [ "$SA" = "running" ] && [ "$SB" = "running" ]; then
    BOTH_RUNNING=$((BOTH_RUNNING + 1))
    [ -n "$FIRST_BOTH" ] || FIRST_BOTH="A=$SA B=$SB (第 $SAMPLES 次采样)"
  fi
  sleep "$SAMPLE_INTERVAL"
done
wait "$PID_A" || die "worker A 退出码非 0 (见 /tmp/parallel_demo_a.log)"
wait "$PID_B" || die "worker B 退出码非 0 (见 /tmp/parallel_demo_b.log)"
echo "采样 $SAMPLES 次, 其中『两个任务同时 running』$BOTH_RUNNING 次"
[ -n "$FIRST_BOTH" ] && echo "首次观测到并行: $FIRST_BOTH"
[ "$BOTH_RUNNING" -gt 0 ] || die "从未观测到两个任务同时运行, 并发演示不成立"

hr "4. 两个 job 的最终状态"
[ "$(run_status "$RUN_A")" = "succeeded" ] || die "run $RUN_A 未成功"
[ "$(run_status "$RUN_B")" = "succeeded" ] || die "run $RUN_B 未成功"
echo "A: run $RUN_A succeeded | B: run $RUN_B succeeded"
echo "两个 job 互不阻塞, 各自跑完 $(printf '%s' "$ITEMS_A") 条用例"

hr "5. A/B 指标对照(第一组对照实验数据)"
for pair in "A:$RUN_A:$TOP_K_A" "B:$RUN_B:$TOP_K_B"; do
  label="${pair%%:*}"; rest="${pair#*:}"; rid="${rest%%:*}"; topk="${rest##*:}"
  curl -sf "$API_BASE/api/v1/runs/$rid" | "$PYTHON_BIN" -c "
import sys, json
run = json.load(sys.stdin)
m = run.get('metrics') or {}
print(f\"$label (top_k=$topk): recall@k={m.get('recall_at_k')} precision@k={m.get('precision_at_k')} \"
      f\"mrr@k={m.get('mrr_at_k')} hit@k={m.get('hit_at_k')} cases={m.get('cases_evaluated')} \"
      f\"hash={run['config_hash'][:8]}\")
"
done

hr "6. 一致性工具的正确反应(不同配置 -> 拒绝做一致性判定)"
set +e
COMPARE_OUT=$("$PYTHON_BIN" -m app.cli.compare_runs --left "$RUN_A" --right "$RUN_B")
COMPARE_CODE=$?
set -e
printf '%s\n' "$COMPARE_OUT" | tail -6
echo "compare_runs 退出码 = $COMPARE_CODE (期望 2: 属于 A/B 场景, 应由 M5 的报告工具处理)"

hr "结论"
echo "并发粒度 = job 级(D12): 同一 job 只能被一个 worker 持有; 多 worker 的并行方式是『多 job』。"
echo "本次两个 worker 各持一个 job, 同时 running 的采样次数 = $BOTH_RUNNING, 两者均跑完。"
echo "run ids: A=$RUN_A (top_k=$TOP_K_A) B=$RUN_B (top_k=$TOP_K_B)"
