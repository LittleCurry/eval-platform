<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NEmpty,
  NGi,
  NGrid,
  NInput,
  NRate,
  NSelect,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { getCaseContext, getRunCaseResults, listRuns } from '../api/runs'
import { listHumanGold, updateHumanGold, upsertHumanGold } from '../api/humanGold'
import type { CaseContextResponse, HumanGoldScore, Run, RunCaseResult } from '../api/types'
import { formatPercent, shortHash } from '../utils/format'
import { flagLabel, primaryFlag, claimLabel, claimTagType } from '../utils/report'
import { usePermission } from '../composables/usePermission'
import {
  GOLD_SHORTCUT_HELP,
  GOLD_VERDICTS,
  goldProgressText,
  goldReviewText,
  goldShortcut,
  goldVerdictLabel,
  goldVerdictType,
  reviewTagType,
} from '../utils/calibration'

const route = useRoute()
const router = useRouter()
const message = useMessage()
// M7-3: 打分是写操作 —— 只读账号看得到金标, 但不该能改
const { canWrite } = usePermission()

const runs = ref<Run[]>([])
const runId = ref<number | null>(route.query.run ? Number(route.query.run) : null)
const cases = ref<RunCaseResult[]>([])
const golds = ref<HumanGoldScore[]>([])
const loading = ref(false)
const errorText = ref('')
const saving = ref(false)
const onlyUnscored = ref(true)
// 标注员名字存本地: 它是金标唯一键的一部分(决定"这是谁判的"), 不该每次重输
const annotator = ref(window.localStorage.getItem('eval.gold.annotator') ?? 'me')
const noteDraft = ref('')
const showContext = ref(false)
const context = ref<CaseContextResponse | null>(null)
const contextLoading = ref(false)

const selectedCaseId = ref<number | null>(null)

const runOptions = computed(() =>
    runs.value.map((run) => ({
      label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)} · ${run.status}`,
      value: run.id,
    })),
)

/** 有判定的 run 才有校准的意义: 默认挑最近一次判过的。 */
async function loadRuns() {
  try {
    runs.value = await listRuns({ limit: 200 })
    if (!runId.value) {
      const judged = runs.value.find((run) => Number(run.metrics?.cases_judged ?? 0) > 0)
      runId.value = judged?.id ?? runs.value[0]?.id ?? null
    }
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  }
}

const goldByCase = computed(() => {
  const map = new Map<number, HumanGoldScore>()
  for (const item of golds.value) {
    if (item.annotator === annotator.value) map.set(item.case_id, item)
  }
  return map
})

interface GoldRow {
  case: RunCaseResult
  gold: HumanGoldScore | null
}

const rows = computed<GoldRow[]>(() =>
    cases.value
        .map((item) => ({ case: item, gold: goldByCase.value.get(item.case_id) ?? null }))
        // 待办优先: 先按"有没有判过"再按 recall 升序(case-results 已经是 recall 升序)
        .filter((row) => (onlyUnscored.value ? row.gold === null : true)),
)

const scoredCount = computed(() => rows.value.filter((row) => row.gold !== null).length)

/** 复核进度按"我"名下的金标算(复核是标注员自己的口径)。 */
const reviewText = computed(() => goldReviewText([...goldByCase.value.values()], scoredCount.value))

const selected = computed(() => {
  if (selectedCaseId.value === null) return null
  return rows.value.find((row) => row.case.case_id === selectedCaseId.value) ?? null
})

async function loadGold() {
  if (!runId.value) return
  loading.value = true
  errorText.value = ''
  try {
    const [caseList, goldList] = await Promise.all([
      getRunCaseResults(runId.value, { limit: 500 }),
      listHumanGold({ runId: runId.value }),
    ])
    cases.value = caseList
    golds.value = goldList
    const stillVisible = rows.value.some((row) => row.case.case_id === selectedCaseId.value)
    if (!stillVisible) selectRow(rows.value[0] ?? null)
    router.replace({ query: { run: String(runId.value) } })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function selectRow(row: GoldRow | null) {
  selectedCaseId.value = row?.case.case_id ?? null
  noteDraft.value = row?.gold?.note ?? ''
  context.value = null
  showContext.value = false
}

/** 打分: 没传的字段服务端保持原值, 所以改判词不会把分数和备注冲掉。 */
async function save(payload: { verdict?: string; relevance?: number; helpfulness?: number; note?: string }) {
  if (!runId.value || !selected.value) return
  const name = annotator.value.trim()
  if (!name) {
    message.error('请先填标注员名字(它是金标的唯一键: 决定这是谁判的)')
    return
  }
  window.localStorage.setItem('eval.gold.annotator', name)
  saving.value = true
  try {
    await upsertHumanGold({
      run_id: runId.value,
      case_id: selected.value.case.case_id,
      annotator: name,
      verdict: payload.verdict ?? selected.value.gold?.verdict ?? 'unclear',
      relevance: payload.relevance,
      helpfulness: payload.helpfulness,
      note: payload.note,
    })
    golds.value = await listHumanGold({ runId: runId.value })
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    saving.value = false
  }
}

/** 判完就自动跳下一题 —— 打分是重复劳动, 主循环必须一次按键完成。 */
async function markVerdict(verdict: string) {
  await save({ verdict })
  next()
}

/**
 * 复核标记: 表示"这条金标被第二个人看过并确认"。
 *
 * 单独一个 PATCH 而不是跟着判词一起提交: 复核是**事后**动作(先自己标完, 再换个人看),
 * 混在打分请求里会让"谁复核的"这件事失去意义。
 */
async function toggleReviewed(reviewed: boolean) {
  const gold = selected.value?.gold
  if (!gold) return
  saving.value = true
  try {
    await updateHumanGold(gold.id, { reviewed })
    golds.value = await listHumanGold({ runId: runId.value as number })
    message.success(reviewed ? '已标记复核' : '已取消复核标记')
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    saving.value = false
  }
}

async function loadContext() {
  if (!selected.value || !runId.value) return
  contextLoading.value = true
  try {
    context.value = await getCaseContext(runId.value, selected.value.case.case_id)
    showContext.value = true
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    contextLoading.value = false
  }
}

function step(delta: number) {
  const list = rows.value
  if (list.length === 0) return
  const index = list.findIndex((row) => row.case.case_id === selectedCaseId.value)
  const target = list[(index + delta + list.length) % list.length]
  if (target) selectRow(target)
}

function next() {
  step(1)
}

function onKeydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  const tag = target?.tagName ?? ''
  // 在备注框里打字时不抢键(否则备注里的 1/2/3 会变成判词)
  if (tag === 'INPUT' || tag === 'TEXTAREA' || target?.isContentEditable) return
  const hit = goldShortcut(event.key)
  if (!hit) return
  event.preventDefault()
  if (hit.kind === 'next') return next()
  if (hit.kind === 'prev') return step(-1)
  void markVerdict(hit.value as string)
}

onMounted(async () => {
  await loadRuns()
  await loadGold()
  window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

/** judge 的断言列表: 打分时要能看到"它凭什么说有/没有幻觉"。 */
const claims = computed(() => selected.value?.case.judge?.claims ?? [])

/**
 * judge 的 rubric 分数只在人工打过分之后才露出来。
 *
 * 为什么: 先给人看"judge 打了 5 分"会锚定他的判断, 校准就变成了自我实现 ——
 * 我们要量的是"人机一致吗", 不是"人会不会跟着机器走"。
 */
const judgeRubricHint = computed(() => {
  const gold = selected.value?.gold
  const rubric = selected.value?.case.judge?.rubric
  if (!gold || !rubric) return null
  if (gold.helpfulness == null && gold.relevance == null) return null
  return `judge 这次给的是 helpfulness ${rubric.helpfulness} / relevance ${rubric.relevance}`
})

function rememberAnnotator() {
  const name = annotator.value.trim()
  if (name) window.localStorage.setItem('eval.gold.annotator', name)
}

const columns: DataTableColumns<GoldRow> = [
  { title: 'qid', key: 'qid', width: 100, render: (row) => row.case.qid },
  {
    title: '人工判定',
    key: 'verdict',
    width: 130,
    render: (row) =>
        h(NTag, { size: 'small', type: goldVerdictType(row.gold?.verdict) },
            { default: () => (row.gold ? goldVerdictLabel(row.gold.verdict) : '未判') }),
  },
  {
    title: '分',
    key: 'score',
    width: 70,
    render: (row) => (row.gold?.helpfulness ? `${row.gold.helpfulness} 分` : '—'),
  },
  {
    title: '复核',
    key: 'reviewed',
    width: 70,
    render: (row) =>
        row.gold
            ? h(NTag, { size: 'small', type: reviewTagType(row.gold.reviewed) },
                { default: () => (row.gold?.reviewed ? '已复核' : '未复核') })
            : '—',
  },
  {
    title: '机器主因',
    key: 'flag',
    width: 150,
    render: (row) => {
      const flag = primaryFlag(row.case.flags)
      return flag ? h(NTag, { size: 'small', type: 'warning' }, { default: () => flagLabel(flag) }) : '—'
    },
  },
  { title: 'Recall', key: 'recall', width: 80, render: (row) => formatPercent(row.case.metrics?.recall) },
  { title: '问题', key: 'question', ellipsis: { tooltip: true }, render: (row) => row.case.question },
]

function onRowProps(row: GoldRow) {
  return { style: 'cursor: pointer', onClick: () => selectRow(row) }
}
</script>

<template>
  <div>
    <NCard title="人工金标打分" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NText depth="3" style="font-size: 12px">只看未打分</NText>
          <NSwitch v-model:value="onlyUnscored" size="small" />
          <NButton size="small" :loading="loading" @click="loadGold">刷新</NButton>
        </NSpace>
      </template>

      <NSpace align="center" :size="12" style="margin-bottom: 12px">
        <NSelect
            v-model:value="runId"
            :options="runOptions"
            filterable
            placeholder="选择一次 run"
            style="width: 340px"
            @update:value="loadGold"
        />
        <NInput
            v-model:value="annotator"
            placeholder="标注员"
            style="width: 140px"
            @blur="rememberAnnotator"
        />
        <NTag v-if="runId" size="small" type="info">{{ goldProgressText(rows.length, scoredCount) }}</NTag>
        <NTag v-if="runId" size="small">{{ reviewText }}</NTag>
        <NButton
            v-if="runId"
            size="small"
            quaternary
            @click="router.push({ path: '/calibration', query: { run: String(runId) } })"
        >
          看校准报告
        </NButton>
      </NSpace>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">{{ errorText }}</NAlert>

      <NGrid :cols="3" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi span="3 s:1">
          <NStatistic label="本次待判题数" :value="String(rows.length)" />
        </NGi>
        <NGi span="3 s:1">
          <NStatistic label="已判" :value="String(scoredCount)" />
        </NGi>
        <NGi span="3 s:1">
          <NStatistic label="全部金标(含其他人)" :value="String(golds.length)" />
        </NGi>
      </NGrid>
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        金标挂在"这次 run 的这道题"上: judge 判的是这次生成的答案, 换一次 run 就要重新标。
        建议挑 30–50 题(含一部分 judge 判过的), 样本太少时 κ 不可解释。
      </NText>
    </NCard>

    <NGrid :cols="5" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
      <NGi span="5 m:3">
        <NCard title="题目列表(按 Recall 升序)" style="margin-bottom: 16px">
          <NDataTable
              :columns="columns"
              :data="rows"
              :loading="loading"
              :row-key="(row: GoldRow) => row.case.case_id"
              :row-props="onRowProps"
              :scroll-x="760"
              :row-class-name="(row: GoldRow) => (row.case.case_id === selectedCaseId ? 'row-active' : '')"
              size="small"
          />
        </NCard>
      </NGi>

      <NGi span="5 m:2">
        <NCard :title="selected ? `打分 · ${selected.case.qid}` : '打分'" style="margin-bottom: 16px">
          <NEmpty v-if="!selected" description="从左边选一道题" />
          <template v-else>
            <NText style="display: block; margin-bottom: 8px">{{ selected.case.question }}</NText>

            <NSpace size="small" style="margin-bottom: 10px">
              <NTag v-if="primaryFlag(selected.case.flags)" size="small" type="warning">
                机器主因：{{ flagLabel(primaryFlag(selected.case.flags) as string) }}
              </NTag>
              <NTag size="small">Recall {{ formatPercent(selected.case.metrics?.recall) }}</NTag>
              <NTag v-if="selected.gold" size="small" :type="goldVerdictType(selected.gold.verdict)">
                已判：{{ goldVerdictLabel(selected.gold.verdict) }}
              </NTag>
            </NSpace>

            <NText depth="3" style="display: block; margin-bottom: 4px; font-size: 12px">模型答案</NText>
            <NCard size="small" style="margin-bottom: 10px; background: rgba(127,127,127,0.06)">
              <NText style="font-size: 13px; white-space: pre-wrap">
                {{ selected.case.answer || '(这次 run 没有生成答案)' }}
              </NText>
            </NCard>

            <NSpace align="center" style="margin-bottom: 10px">
              <NButton size="tiny" quaternary :loading="contextLoading" @click="loadContext">
                取检索上下文正文
              </NButton>
              <NText depth="3" style="font-size: 12px">
                上下文只在向量库有一份(不落库), 点开时按需取
              </NText>
            </NSpace>

            <NCard v-if="showContext && context" size="small" style="margin-bottom: 10px">
              <NAlert v-if="context.error" type="warning" :show-icon="false" style="margin-bottom: 8px">
                {{ context.error }}
              </NAlert>
              <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">
                集合 {{ context.collection }}
              </NText>
              <div v-for="(chunk, index) in context.chunks" :key="chunk.point_id" style="margin-bottom: 8px">
                <NText depth="3" style="font-size: 12px">
                  #{{ index + 1 }} · {{ chunk.doc_id }} · {{ chunk.section }} · score
                  {{ (chunk.score ?? 0).toFixed(3) }}
                </NText>
                <NText style="display: block; font-size: 12px; white-space: pre-wrap">
                  {{ chunk.found ? chunk.text : '（正文缺失: 集合可能被重建过）' }}
                </NText>
              </div>
            </NCard>

            <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">
              judge 的断言（{{ claims.length }} 条）
            </NText>
            <div v-if="claims.length === 0" style="margin-bottom: 10px">
              <NText depth="3" style="font-size: 12px">
                这次 run 没有判定结论（或答案没有可核查断言）—— 那就只能靠人工判断了
              </NText>
            </div>
            <div v-for="claim in claims" :key="claim.text" style="margin-bottom: 6px">
              <NTag size="tiny" :type="claimTagType(claim.label)">
                {{ claimLabel(claim.label) }}
              </NTag>
              <NText style="font-size: 12px; margin-left: 6px">{{ claim.text }}</NText>
              <NText v-if="claim.evidence" depth="3" style="display: block; font-size: 12px">
                依据：{{ claim.evidence }}
              </NText>
            </div>

            <NText depth="3" style="display: block; margin: 12px 0 6px; font-size: 12px">
              人工判定（1/2/3）
            </NText>
            <NSpace :size="6" style="margin-bottom: 12px">
              <NButton
                  v-for="(verdict, index) in GOLD_VERDICTS"
                  :key="verdict"
                  size="small"
                  :type="selected.gold?.verdict === verdict ? 'primary' : 'default'"
                  :loading="saving"
                  :disabled="!canWrite"
                  @click="markVerdict(verdict)"
              >
                {{ index + 1 }} {{ goldVerdictLabel(verdict) }}
              </NButton>
            </NSpace>

            <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">
              有用性 helpfulness（可后补）
            </NText>
            <NRate
                :value="selected.gold?.helpfulness ?? 0"
                :count="5"
                size="small"
                style="margin-bottom: 10px"
                :readonly="!canWrite"
                @update:value="(value: number) => save({ helpfulness: value })"
            />

            <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">
              相关性 relevance（可后补）
            </NText>
            <NRate
                :value="selected.gold?.relevance ?? 0"
                :count="5"
                size="small"
                style="margin-bottom: 10px"
                :readonly="!canWrite"
                @update:value="(value: number) => save({ relevance: value })"
            />

            <NAlert v-if="judgeRubricHint" type="info" :show-icon="false" style="margin-bottom: 10px">
              <NText style="font-size: 12px">
                {{ judgeRubricHint }}（打完分才显示, 免得锚定你的判断）
              </NText>
            </NAlert>

            <NSpace align="center" style="margin-bottom: 10px">
              <NText depth="3" style="font-size: 12px">复核（换个人看过并确认）</NText>
              <NSwitch
                  :value="selected.gold?.reviewed ?? false"
                  :disabled="!selected.gold || !canWrite"
                  size="small"
                  @update:value="toggleReviewed"
              />
              <NTag v-if="selected.gold" size="small" :type="reviewTagType(selected.gold.reviewed)">
                {{ selected.gold.reviewed ? '已复核' : '未复核' }}
              </NTag>
              <NText v-if="!selected.gold" depth="3" style="font-size: 12px">先判定后才能标记复核</NText>
            </NSpace>

            <NInput
                v-model:value="noteDraft"
                type="textarea"
                :autosize="{ minRows: 2, maxRows: 4 }"
                placeholder="备注：这题为什么这么判（校准报告里会带出来看）"
                style="margin-bottom: 8px"
            />
            <NSpace justify="end">
              <NButton size="small" :loading="saving" :disabled="!canWrite" @click="save({ note: noteDraft })">
                保存备注
              </NButton>
              <NButton size="small" quaternary @click="step(-1)">上一题 (b)</NButton>
              <NButton size="small" type="primary" quaternary @click="next">下一题 (n)</NButton>
            </NSpace>
          </template>
        </NCard>
      </NGi>
    </NGrid>

    <NCard size="small">
      <NText depth="3" style="font-size: 12px">{{ GOLD_SHORTCUT_HELP }}</NText>
    </NCard>
  </div>
</template>

<style scoped>
:deep(.row-active td) {
  background: rgba(127, 127, 127, 0.12);
}
</style>
