#!/usr/bin/env bash
#
# 20 分钟演示脚本(M7-6)。
#
# 设计原则: **每一步都先把"预期现象"打出来, 再打真实返回值**, 并当场对比。
# 这样录屏的人不用背台词, 看的人也不必"相信我讲的" —— 屏幕上是这次真的跑出来的数字。
#
# 两种模式:
#   默认(演示路径)  不花 token: 直接用库里已有的 run 走"对比 → 报告 → 标注闭环 → 校准",
#                   同时把每一步的结论打印出来。适合录屏/给同事看。
#   --full          真跑一遍: 建项目 → 建语料库 → 上传文档 → 建数据集 → 提交一次
#                   **纯检索**评测(top_k=1, 不启用生成与判定 → 不烧 LLM token, 只花
#                   embedding) → 等 worker 跑完 → 报告 → 改 top_k=3 重跑 → 对比。
#                   结尾打印"这次真的修好了 N 题", 但**不自动删**任何东西。
#
# 用法:
#   scripts/demo.sh                                   # 演示路径(默认)
#   scripts/demo.sh --full                            # 从零跑一遍(纯检索, 不烧生成/判定)
#   scripts/demo.sh --run 155 --baseline 112 --candidate 100
#   API=http://127.0.0.1:8080 WEB=http://localhost:5173 scripts/demo.sh
#
# 需要: curl + jq; 一个能登录的账号(默认取 .env 里的 DEMO_EMAIL/DEMO_PASSWORD,
# 也可以用环境变量传)。找不到账号时脚本会告诉你怎么办, 不会瞎猜。

set -euo pipefail

cd "$(dirname "$0")/.."

API="${API:-http://127.0.0.1:8080}"
WEB="${WEB:-http://localhost:5173}"
# 演示用的 run(README 的 20 分钟 demo 用的就是这三次: 同一份数据、不同配置)
DEMO_RUN="${DEMO_RUN:-155}"            # k=5 + 生成 + 判定(60 题)
BASELINE_RUN="${BASELINE_RUN:-112}"    # k=1
CANDIDATE_RUN="${CANDIDATE_RUN:-100}"  # k=5
FULL=0

while [ $# -gt 0 ]; do
  case "$1" in
    --full) FULL=1 ;;
    --run) DEMO_RUN="$2"; shift ;;
    --baseline) BASELINE_RUN="$2"; shift ;;
    --candidate) CANDIDATE_RUN="$2"; shift ;;
    -h|--help) sed -n '3,26p' "$0"; exit 0 ;;
    *) echo "未知参数: $1(用 --help 看用法)"; exit 2 ;;
  esac
  shift
done

# ---- 输出小工具: 步骤 / 预期 / 实际 ----
BOLD=$'\033[1m'; DIM=$'\033[2m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; OFF=$'\033[0m'
STEP_NO=0
step()  { STEP_NO=$((STEP_NO + 1)); printf '\n%s── 第 %d 步 · %s%s\n' "$BOLD" "$STEP_NO" "$*" "$OFF"; }
note()  { printf '%s   %s%s\n' "$DIM" "$*" "$OFF"; }
want()  { printf '%s   预期: %s%s\n' "$YELLOW" "$*" "$OFF"; }
got()   { printf '%s   实际: %s%s\n' "$GREEN" "$*" "$OFF"; }
warn()  { printf '%s   ⚠️  %s%s\n' "$RED" "$*" "$OFF"; }
ok()    { printf '%s   ✅ %s%s\n' "$GREEN" "$*" "$OFF"; }

# ---- 账号: 从 .env 或环境变量取, 不猜 ----
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set -a; . ./.env; set +a
fi
EMAIL="${DEMO_EMAIL:-${EMAIL:-}}"
PASSWORD="${DEMO_PASSWORD:-${PASSWORD:-}}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "缺少依赖: $1"; exit 1; }; }
need curl
need jq

# 等一次 run 跑完(worker 在容器里常驻时, 本地脚本不需要自己跑 worker, 但要等它)
wait_run() { # wait_run RUN_ID [超时秒数]
  local run_id="$1" timeout="${2:-180}" waited=0 status="unknown"
  while [ "$waited" -lt "$timeout" ]; do
    status=$(api GET "/api/v1/runs/$run_id/progress" | jq -r '.status // "unknown"')
    case "$status" in
      succeeded|failed) printf '%s' "$status"; return 0 ;;
    esac
    sleep 3; waited=$((waited + 3))
  done
  printf '%s' "$status"
}

api() { # api METHOD PATH [BODY]  → 打印响应体, 非 2xx 时退出
  local method="$1" path="$2" body="${3:-}"
  local out status
  if [ -n "$body" ]; then
    out=$(curl -sS -m 30 -w $'\n%{http_code}' -X "$method" "$API$path" \
      -H "Authorization: Bearer ${TOKEN:-}" -H 'Content-Type: application/json' -d "$body")
  else
    out=$(curl -sS -m 30 -w $'\n%{http_code}' -X "$method" "$API$path" \
      -H "Authorization: Bearer ${TOKEN:-}")
  fi
  status=$(printf '%s' "$out" | tail -n1)
  out=$(printf '%s' "$out" | sed '$d')
  if [ "${status:0:1}" != "2" ]; then
    warn "$method $path → HTTP $status: $out"
    return 1
  fi
  printf '%s' "$out"
}

echo "${BOLD}评测平台演示(M7-6)${OFF}"
note "API=$API  WEB=$WEB  模式=$([ "$FULL" = 1 ] && echo '从零跑一遍(--full)' || echo '演示路径(不花 token)')"

# ============================================================================
step "环境自检: 服务是不是真的起来了"
want "/healthz 报 postgres 与 qdrant 都 ok(容器探针和反代也靠它)"
HEALTH=$(curl -sS -m 5 "$API/healthz" || true)
if [ -z "$HEALTH" ]; then
  warn "连不上 $API —— 先 make up-deps && make migrate-up && make api-restart"
  exit 1
fi
got "$(printf '%s' "$HEALTH" | jq -c .)"
printf '%s' "$HEALTH" | jq -e '.checks // .status // empty | tostring | test("ok|up|true")' >/dev/null 2>&1 \
  && ok "依赖连通" || note "(响应结构随版本, 不阻断演示)"

step "登录拿 token"
if [ -z "$EMAIL" ] || [ -z "$PASSWORD" ]; then
  warn "没有账号: 用 DEMO_EMAIL=you@example.com DEMO_PASSWORD=*** scripts/demo.sh 再跑,"
  note "或在 .env 里加 DEMO_EMAIL / DEMO_PASSWORD。库里还没有账号时先在 $WEB/login 注册第一个管理员。"
  exit 1
fi
LOGIN=$(api POST /api/v1/auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASSWORD" '{email:$e,password:$p}')")
TOKEN=$(printf '%s' "$LOGIN" | jq -r '.token')
ROLE=$(printf '%s' "$LOGIN" | jq -r '.user.role')
GOT_EMAIL=$(printf '%s' "$LOGIN" | jq -r '.user.email')
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || { warn "登录没拿到 token: $LOGIN"; exit 1; }
got "已登录 $GOT_EMAIL(角色 $ROLE)"
note "token 是 HS256 JWT, 12 小时有效; 服务端不存会话 —— 停用账号下一次请求就 401(D22)"

step "数据概览: 现在库里有什么"
RUNS=$(api GET "/api/v1/runs?limit=200")
PROJECTS=$(api GET /api/v1/projects)
got "项目 $(printf '%s' "$PROJECTS" | jq 'length') 个 · 运行记录 $(printf '%s' "$RUNS" | jq 'length') 条"
printf '%s' "$RUNS" | jq -r '.[:5][] | "     #\(.id)  dataset \(.dataset_id)  \(.status)  recall=\(.metrics.recall_at_k // "—")  k=\(.metrics.k // "—")  \(.config_hash[0:8])"' 2>/dev/null || true
note "每条 run 都带 git_sha + config_hash + 数据 id(复现性四件套)"

step "定位: 把两次配置的差异摊出来(A/B 对比)"
want "recall 上升、p 值远小于噪声、fixed 很多而 broke 为 0 —— 改动真的修好了题, 且不是运气"
CMP=$(api GET "/api/v1/compare?left=$BASELINE_RUN&right=$CANDIDATE_RUN")
if [ "$(printf '%s' "$CMP" | jq -r '.comparable')" != "true" ]; then
  warn "不可比: $(printf '%s' "$CMP" | jq -r '.reason // "原因未给出"')"
  note "(跨评测集/跨语料时平台会直接拒绝给结论, 而不是硬算一个数 —— 这是设计, 不是 bug)"
else
  printf '%s' "$CMP" | jq -r '
    .summary
    | to_entries[]
    | select(.value.cases > 0)
    | "     \(.key): \(.value.left) → \(.value.right)  Δ\(.value.delta)  p=\(.value.p_value)  \(if .value.significant then "显著" else "不显著" end)"'
  printf '%s' "$CMP" | jq -r '"     修好 \(.fixed | length) 题 · 变坏 \(.broke | length) 题 · 噪声地板 \(.noise_floor)"'
  printf '%s' "$CMP" | jq -r '.by_flag[]? | "     按归因: \(.key)  \(.cases) 题, 平均 Δ\(.mean_delta)(修好 \(.improved), 变坏 \(.worsened))"'
  note "precision 下降是算术必然: k 从 1 涨到 5, 分母(捞回来的 chunk 数)变大 —— 看指标要看口径, 别只看箭头"
  NOTE=$(printf '%s' "$CMP" | jq -r '.generation_note // ""')
  [ -n "$NOTE" ] && note "$NOTE"
fi
note "对照页: $WEB/compare?left=$BASELINE_RUN&right=$CANDIDATE_RUN"

step "报告: 哪句答案在编(生成侧判定)"
REPORT=$(api GET "/api/v1/runs/$DEMO_RUN/report?limit=10")
printf '%s' "$REPORT" | jq -r '
  .metrics as $m
  | "     recall@k=\($m.recall_at_k // "—")  mrr@k=\($m.mrr_at_k // "—")  hit@k=\($m.hit_at_k // "—")  k=\($m.k // "—")"
  + (if ($m.cases_judged // 0) > 0
     then "\n     幻觉率=\($m.hallucination_rate)  断言支持率=\($m.claim_support_rate)  无关率=\($m.irrelevant_rate)"
          + "\n     断言 \($m.claims_supported)/\($m.claims_total) 有据 · 判定 \($m.cases_judged) 题 · judge token \(($m.judge_prompt_tokens // 0) + ($m.judge_completion_tokens // 0))"
     else "\n     (这次没启用生成/判定 → 报告里不出现三率, 见 D14)" end)'
printf '%s' "$REPORT" | jq -r '"     归因标签: " + ((.flag_counts // {}) | to_entries | map("\(.key)×\(.value)") | join("  "))'
note "三率之和必须为 1(平台自己校验, 不成立会在报告页顶部报警)"
note "flags[0] = 主因: 检索没命中算 retrieval_miss, 命中却答错算 hallucination/generation_quality(D16)"
note "对照页: $WEB/runs/$DEMO_RUN  (表格「主因」列 + 点任意一题看断言逐条证据 + 导出 CSV/Markdown)"

step "闭环: 标注 → 改配置重跑 → 逐题核对是否真的修好"
CLOSURE=$(api GET "/api/v1/closure?baseline=$BASELINE_RUN&candidate=$CANDIDATE_RUN")
printf '%s' "$CLOSURE" | jq -r '
  .summary as $s
  | "     标注 \($s.annotated) 条(已修 \($s.fixed_total)): 修好 \($s.improved) · 没变 \($s.stable) · 变坏 \($s.worsened) · 说不清 \($s.unverifiable)"
  + (if $s.annotated == 0 then "\n     (这次还没有标注 → 下面是「该标哪几题」)" else "" end)'
if [ "$(printf '%s' "$CLOSURE" | jq -r '.summary.annotated')" = "0" ]; then
  note "在 $WEB/annotations?run=$BASELINE_RUN 把这几题标成「已修」(它们正是 k=1 时检索没命中的题):"
  api GET "/api/v1/runs/$BASELINE_RUN/case-results?limit=200&flagged=1" \
    | jq -r '[.[] | select(.flags[0] == "retrieval_miss")][:5][] | "       \(.qid)   recall=\(.metrics.recall)"'
  note "标完再跑一次: scripts/demo.sh  (或用 --full 让脚本自己走完整条链路)"
else
  printf '%s' "$CLOSURE" | jq -r '
    .records[:5][]?
    | "     \(.qid): \(.status) → \(.verdict)  "
      + ([.evidence[] | select(.comparable) | "\(.metric) \(.left)→\(.right)"] | join(" · "))'
  note "闭环回答的是「我改的东西到底修好了哪几题、有没有修坏别的题」—— 靠逐题证据, 不靠感觉"
fi
note "对照页: $WEB/closure?baseline=$BASELINE_RUN&candidate=$CANDIDATE_RUN"

step "judge 可信度: 它自己准不准"
CAL=$(api GET "/api/v1/judge-calibration?run_id=$DEMO_RUN")
printf '%s' "$CAL" | jq -r '
  "     金标 \(.cases_with_gold)/\(.total_cases) 题(覆盖率 \((.coverage * 100) | floor)%) · 已判定 \(.gold_judged) · 标注员 \((.annotators // []) | join(",") | if . == "" then "无" else . end)"
  + (if .binary then "\n     二分类校准: \(.binary.pairs) 对, κ=\(.binary.kappa)" else "\n     还没有金标 → 平台不给 κ(而不是给一个漂亮的假数)" end)
  + (if .helpfulness then "\n     分数校准: MAE=\(.helpfulness.mae)  恰好相同 \(.helpfulness.exact_agreement)  差 1 分内 \(.helpfulness.within_1)" else "" end)
  + "\n     人机不一致 \((.disagreements // []) | length) 题"'
printf '%s' "$CAL" | jq -r '.notes[]? | "     ⚠️  \(.)"'
note "对照页: $WEB/gold?run=$DEMO_RUN(打金标) → $WEB/calibration?run=$DEMO_RUN(看 κ/混淆矩阵/判错清单)"

step "可靠性: 崩了也能续跑(不花 token)"
note "真正演示这条命令是: make drill-compare REF=$CANDIDATE_RUN"
note "它会 kill -9 worker 再重启, 最后逐题与参照 run 比对; 退出码 0 = 逐题一致"
note "(这条比较慢, 演示时按需运行, 脚本不替你跑)"

# ============================================================================
if [ "$FULL" = 1 ]; then
  echo
  echo "${BOLD}═══ --full: 从零跑一遍(纯检索, 不烧生成/判定 token) ═══${OFF}"
  SUFFIX="$(date +%m%d-%H%M%S)"

  step "建项目"
  PROJECT=$(api POST /api/v1/projects "$(jq -nc --arg n "demo-$SUFFIX" '{name:$n,description:"scripts/demo.sh 自动创建"}')")
  PROJECT_ID=$(printf '%s' "$PROJECT" | jq -r '.id')
  got "项目 #$PROJECT_ID $(printf '%s' "$PROJECT" | jq -r '.name')(创建者: $(printf '%s' "$PROJECT" | jq -r '.owner_email // "—"'))"

  step "建语料库 + 上传文档"
  CORPUS=$(api POST /api/v1/corpora "$(jq -nc --argjson p "$PROJECT_ID" '{project_id:$p,name:"demo 帮助文档",source_type:"manual"}')")
  CORPUS_ID=$(printf '%s' "$CORPUS" | jq -r '.id')
  # 演示数据是**故意设计过**的, 为的是让"改进"这一步真的有东西可看:
  # - D4 满篇讲"虚拟商品"但不说退款 —— 一个像模像样的干扰项;
  # - Q4 是跨文档问题(答案分散在 D1 与 D2 里), gold_anchors 有两篇 →
  #   top_k=1 时**必然**答不全(最多捞 1 个 gold, recall ≤ 0.5), top_k=3 才可能捞齐。
  # 这样 k=1 → k=3 的对比不是"碰巧变好", 而是配置决定的必然结果。
  DOCS=$(jq -nc '[{
      doc_id:"D1", title:"退款政策",
      raw_text:"退款政策：购买后 7 天内可无理由退款。超过 7 天但未满 30 天的订单，需要客服审核，审核通过后 3 个工作日到账。虚拟商品一经激活不支持退款。"
    },{
      doc_id:"D2", title:"发票与开票",
      raw_text:"发票：订单完成后可在订单详情页申请电子发票，一般 24 小时内开出。发票抬头可以修改一次。纸质发票需联系客服并承担快递费。"
    },{
      doc_id:"D3", title:"账号与安全",
      raw_text:"账号安全：支持手机号与邮箱两种登录方式。连续 5 次密码错误会锁定 15 分钟。修改密码后其他设备上的登录会失效。"
    },{
      doc_id:"D4", title:"虚拟商品说明",
      raw_text:"虚拟商品说明：虚拟商品包括会员卡、点券与各类激活码，一经激活即视为已使用。虚拟商品不支持转赠，不支持跨账号迁移，也不支持更换绑定的手机号。"
    }]')
  api POST "/api/v1/corpora/$CORPUS_ID/documents" "$(jq -nc --argjson d "$DOCS" '{documents:$d}')" >/dev/null
  got "语料库 #$CORPUS_ID 已上传 4 篇(其中 D4 是故意放的干扰项)"
  note "切分与向量化由 worker 按 run 的快照配置做 —— 同一份原文, 换个切分参数就是另一套 chunk"

  step "建数据集 + 4 条用例(JSONL 导入, 带 gold_anchors)"
  DATASET=$(api POST /api/v1/datasets "$(jq -nc --argjson p "$PROJECT_ID" '{project_id:$p,name:"demo 评测集",description:"3 题小样"}')")
  DATASET_ID=$(printf '%s' "$DATASET" | jq -r '.id')
  JSONL=$(jq -nc '
    {qid:"Q1",question:"退款要多久到账？",difficulty:"易",gold_anchors:[{doc:"D1"}]},
    {qid:"Q2",question:"怎么开发票？",difficulty:"易",gold_anchors:[{doc:"D2"}]},
    {qid:"Q3",question:"密码输错几次会被锁？",difficulty:"中",gold_anchors:[{doc:"D3"}]},
    {qid:"Q4",question:"退款到账后怎么开发票？",difficulty:"难",gold_anchors:[{doc:"D1"},{doc:"D2"}]}')
  IMPORT=$(api POST "/api/v1/datasets/$DATASET_ID/cases/import" "$JSONL")
  got "数据集 #$DATASET_ID: 导入 $(printf '%s' "$IMPORT" | jq -r '.imported')/$(printf '%s' "$IMPORT" | jq -r '.total') 条, 错误 $(printf '%s' "$IMPORT" | jq -r '.errors | length')"
  note "gold_anchors 是「正确答案必须来自哪篇文档」, 检索指标全靠它(D4); 导入是逐行宽松解析, 坏行会带行号回报而不是整批失败"

  step "提交评测 #1: 纯检索, top_k=1"
  want "建 run + job + 每题的 job_items, HTTP 立刻返回(不等评测跑完)"
  RUN1=$(api POST /api/v1/runs "$(jq -nc --argjson d "$DATASET_ID" --argjson c "$CORPUS_ID" --argjson p "$PROJECT_ID" \
    '{dataset_id:$d,corpus_id:$c,project_id:$p,top_k:1}')")
  RUN1_ID=$(printf '%s' "$RUN1" | jq -r '.run_id')
  JOB1_ID=$(printf '%s' "$RUN1" | jq -r '.job_id')
  got "run #$RUN1_ID(job #$JOB1_ID)已入队 · 配置指纹 $(printf '%s' "$RUN1" | jq -r '.config_hash[0:8]')… · 集合 $(printf '%s' "$RUN1" | jq -r '.collection')"
  note "不传 generation/judge = 只跑检索: 快照里也不会写这两段, 复现时不会误以为跑过(D14)"

  step "让 worker 跑完这次任务"
  note "常驻 worker: make worker  |  容器: docker compose run --rm worker python -m app.cli.worker --once"
  if ! api GET "/api/v1/runs/$RUN1_ID/progress" | jq -e '.status == "succeeded"' >/dev/null 2>&1; then
    (cd worker && .venv/bin/python -m app.cli.worker --once --job-id "$JOB1_ID" >/tmp/demo-worker.log 2>&1) \
      || note "本地 worker 没跑起来(先 make worker-setup / 或 make worker 常驻)"
    # 首次提交会按 run 的切分配置自动建索引(以前只有 CLI 入口, 界面上跑不动)
    if grep -q index_build_start /tmp/demo-worker.log; then
      got "worker 自动建了索引: $(grep -o '"collection": "[^"]*"' /tmp/demo-worker.log | head -1)"
      note "worker 日志: /tmp/demo-worker.log(含 index_build_start / index_build_done 两行)"
    fi
  fi
  RUN1_STATUS=$(wait_run "$RUN1_ID")
  got "run #$RUN1_ID 状态: $RUN1_STATUS"
  api GET "/api/v1/runs/$RUN1_ID/report?limit=5" \
    | jq -r '"     recall@k=\(.metrics.recall_at_k // "—")  mrr@k=\(.metrics.mrr_at_k // "—")  hit@k=\(.metrics.hit_at_k // "—")"'
  api GET "/api/v1/runs/$RUN1_ID/report?limit=5" \
    | jq -r '"     归因: " + ((.flag_counts // {}) | to_entries | map("\(.key)×\(.value)") | join("  ") | if . == "" then "(无标签 = 检索全命中)" else . end)'

  step "标注: 把 #RUN1 里检索没命中的题标成「已修」"
  want "标注挂在 run 上(D20): 它记录的是「我当时判定这次 run 的这几题是坏的」"
  # 取"检索类主因"的题(没命中 / 只命中一部分 / 命中但排太后): 这三类都算检索有问题
  MISS=$(api GET "/api/v1/runs/$RUN1_ID/case-results?limit=200&flagged=1" \
    | jq -r '[.[] | select((.flags[0] // "") | startswith("retrieval"))][:3][] | "\(.case_id) \(.qid) \(.flags[0])"')
  if [ -z "$MISS" ]; then
    note "这次 run 没有检索类标签(全都命中了) —— 闭环那步就没有可核对的题, 属正常"
  else
    while read -r case_id qid flag; do
      [ -n "$case_id" ] || continue
      api POST /api/v1/annotations "$(jq -nc --argjson r "$RUN1_ID" --argjson c "$case_id" \
        '{run_id:$r,case_id:$c,status:"fixed",reason:"retrieval",comment:"demo: 提升 top_k 应该能捞回来"}')" >/dev/null
      got "已标 #$case_id $qid($flag)→ 已修(归因: 检索问题)"
    done <<< "$MISS"
  fi

  step "提交评测 #2: 只改 top_k 1 → 3"
  want "两行配置只有 top_k 不同 → 配置指纹必然不同(指纹是复现的锚点)"
  RUN2=$(api POST /api/v1/runs "$(jq -nc --argjson d "$DATASET_ID" --argjson c "$CORPUS_ID" --argjson p "$PROJECT_ID" \
    '{dataset_id:$d,corpus_id:$c,project_id:$p,top_k:3}')")
  RUN2_ID=$(printf '%s' "$RUN2" | jq -r '.run_id')
  JOB2_ID=$(printf '%s' "$RUN2" | jq -r '.job_id')
  got "run #$RUN2_ID 已入队 · 配置指纹 $(printf '%s' "$RUN2" | jq -r '.config_hash[0:8]')…(与 #$RUN1_ID 不同)"
  api GET "/api/v1/runs/$RUN2_ID/progress" | jq -e '.status == "succeeded"' >/dev/null 2>&1 \
    || (cd worker && .venv/bin/python -m app.cli.worker --once --job-id "$JOB2_ID" >>/tmp/demo-worker.log 2>&1) \
    || note "本地 worker 没跑起来(容器 worker 在场时会自己领走)"
  if grep -c index_build_start /tmp/demo-worker.log 2>/dev/null | grep -qx 1; then
    note "第二次提交没有重新建索引(集合已经在了) —— 日志里只有一次 index_build_start"
  fi
  RUN2_STATUS=$(wait_run "$RUN2_ID")
  got "run #$RUN2_ID 状态: $RUN2_STATUS"

  step "对比这两次: 改动值不值"
  api GET "/api/v1/compare?left=$RUN1_ID&right=$RUN2_ID" | jq -r '
    if .comparable then
      (.summary | to_entries[] | select(.value.cases > 0)
       | "     \(.key): \(.value.left) → \(.value.right)  Δ\(.value.delta)  p=\(.value.p_value)")
      + "     修好 \(.fixed | length) 题 · 变坏 \(.broke | length) 题 · 噪声地板 \(.noise_floor)"
    else "     不可比: \(.reason // "")" end'
  note "对照页: $WEB/compare?left=$RUN1_ID&right=$RUN2_ID"

  step "闭环: 我标的那几题, 这次真的修好了吗"
  CLOSURE=$(api GET "/api/v1/closure?baseline=$RUN1_ID&candidate=$RUN2_ID")
  printf '%s' "$CLOSURE" | jq -r '
    .summary as $s
    | "     标注 \($s.annotated) 条: 修好 \($s.improved) · 没变 \($s.stable) · 变坏 \($s.worsened) · 说不清 \($s.unverifiable)  (可销单 \($s.eligible_for_verify))"'
  printf '%s' "$CLOSURE" | jq -r '
    .records[]?
    | "     \(.qid): \(.status) → \(.verdict)  "
      + ([.evidence[] | select(.comparable) | "\(.metric) \(.left)→\(.right)"] | join(" · "))'
  note "闭环页: $WEB/closure?baseline=$RUN1_ID&candidate=$RUN2_ID(可「一键验证」推进到 verified)"
  note "演示路径走完。留下的 run #$RUN1_ID / #${RUN2_ID}、项目 #$PROJECT_ID 与那几条标注不会被自动清理 ——"
  note "想删就自己在界面上删(删除是 admin 动作, 不可逆)。"
fi


echo "${BOLD}演示结束${OFF}"
note "完整回归(不花 token): make regress    ← Go + worker + 前端 + 构建 + 迁移检查"
note "文档: README「20 分钟 demo」/ docs/deploy.md / process.md(每一步的取舍与踩坑)"
