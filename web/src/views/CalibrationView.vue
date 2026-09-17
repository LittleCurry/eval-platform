<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NEmpty,
  NGi,
  NGrid,
  NSelect,
  NSpace,
  NStatistic,
  NTag,
  NText,
} from 'naive-ui'
import { listRuns } from '../api/runs'
import { getJudgeCalibration } from '../api/humanGold'
import type { CalibrationDisagreement, GoldScoreCalibration, JudgeCalibration, Run } from '../api/types'
import { shortHash } from '../utils/format'
import {
  biasText,
  binaryConfusionCells,
  disagreementEmptyHint,
  disagreementLabel,
  disagreementSummary,
  disagreementTagType,
  calibrationVerdict,
  confusionMax,
  coverageText,
  heatIntensity,
  interAnnotatorText,
  isCalibrationUsable,
  kappaBand,
  notesWithSeverity,
  scoreLine,
} from '../utils/calibration'

const route = useRoute()
const router = useRouter()

const runs = ref<Run[]>([])
const runId = ref<number | null>(route.query.run ? Number(route.query.run) : null)
const annotatorFilter = ref<string>('')
const report = ref<JudgeCalibration | null>(null)
const loading = ref(false)
const errorText = ref('')

const runOptions = computed(() =>
    runs.value.map((run) => ({
      label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)} · ${run.status}`,
      value: run.id,
    })),
)

const annotatorOptions = computed(() => [
  { label: '全部标注员', value: '' },
  ...(report.value?.annotators ?? []).map((name) => ({ label: name, value: name })),
])

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

async function loadReport() {
  if (!runId.value) return
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getJudgeCalibration({
      runId: runId.value,
      annotator: annotatorFilter.value || undefined,
    })
    router.replace({ query: { run: String(runId.value) } })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await loadRuns()
  await loadReport()
})

const verdict = computed(() => (report.value ? calibrationVerdict(report.value) : null))
const usable = computed(() => (report.value ? isCalibrationUsable(report.value) : false))
const confusions = computed(() => (report.value?.binary ? binaryConfusionCells(report.value.binary) : []))
const notes = computed(() => notesWithSeverity(report.value?.notes ?? []))
const disagreements = computed(() => report.value?.disagreements ?? [])

/**
 * 判错清单的直接动作: 去报告页看这道题(答案 + 断言逐条 + 上下文正文)。
 * 校准报告的价值就在"点得进去"—— 只给 κ 数字, 人不知道 prompt 该改哪里。
 */
function openCase(row: CalibrationDisagreement) {
  if (!runId.value) return
  router.push({ path: `/runs/${runId.value}`, query: { case: String(row.case_id) } })
}

const disagreementColumns: DataTableColumns<CalibrationDisagreement> = [
  { title: 'qid', key: 'qid', width: 110, render: (row) => row.qid || `case ${row.case_id}` },
  {
    title: '错法',
    key: 'kind',
    width: 200,
    render: (row) =>
        h(NTag, { size: 'small', type: disagreementTagType(row.kind) },
            { default: () => disagreementLabel(row.kind) }),
  },
  {
    title: '人工判定',
    key: 'human',
    width: 140,
    render: (row) => (row.human_verdict === 'hallucinated' ? '有幻觉' : '忠实'),
  },
  {
    title: 'judge 的无据断言数',
    key: 'unsupported',
    width: 160,
    render: (row) => String(row.judge_unsupported),
  },
  {
    title: '操作',
    key: 'action',
    width: 120,
    render: (row) =>
        h(NButton, { size: 'tiny', quaternary: true, onClick: () => openCase(row) },
            { default: () => '看这道题' }),
  },
]
const interText = computed(() => (report.value ? interAnnotatorText(report.value) : null))

/** 分数校准的两张表: usefulness 与 relevance 各一张 5×5。 */
interface ScoreSection {
  key: string
  title: string
  hint: string
  score: GoldScoreCalibration
}

const scoreSections = computed<ScoreSection[]>(() => {
  const list: ScoreSection[] = []
  if (report.value?.helpfulness) {
    list.push({
      key: 'helpfulness',
      title: 'helpfulness（有用性）',
      hint: 'judge 的 rubric 分数 vs 人工打分',
      score: report.value.helpfulness,
    })
  }
  if (report.value?.relevance) {
    list.push({
      key: 'relevance',
      title: 'relevance（相关性）',
      hint: 'judge 的 rubric 分数 vs 人工打分',
      score: report.value.relevance,
    })
  }
  return list
})

const maxConfusion = computed(() => {
  let max = 0
  for (const section of scoreSections.value) {
    max = Math.max(max, confusionMax(section.score))
  }
  return max
})

/** 5×5 表格: 行 = 人工, 列 = judge。 */
const scoreColumns: DataTableColumns<{ human: number; counts: number[] }> = [
  { title: '人工 \\ judge', key: 'human', width: 110, render: (row) => `${row.human} 分` },
  ...[1, 2, 3, 4, 5].map((judgeScore, index) => ({
    title: `${judgeScore} 分`,
    key: `j${judgeScore}`,
    width: 76,
    align: 'center' as const,
    render: (row: { human: number; counts: number[] }) => {
      const count = row.counts[index]
      const max = maxConfusion.value
      const intensity = heatIntensity(count, max)
      // 对角线(人机同分)单独描边: 一眼看出"落在同一格的比例"
      const diagonal = index + 1 === row.human
      return h(
        'div',
        {
          style: {
            padding: '2px 4px',
            borderRadius: '4px',
            background: intensity > 0
                ? `rgba(24, 160, 88, ${0.12 + intensity * 0.5})`
                : 'transparent',
            fontWeight: diagonal && count > 0 ? 700 : 400,
          },
        },
        count === 0 ? '·' : String(count),
      )
    },
  })),
]

function scoreRows(score: GoldScoreCalibration): { human: number; counts: number[] }[] {
  return [1, 2, 3, 4, 5].map((human) => ({
    human,
    counts: score.confusion[human - 1] ?? [0, 0, 0, 0, 0],
  }))
}
</script>

<template>
  <div>
    <NCard title="Judge 校准报告" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NButton size="small" :loading="loading" @click="loadReport">刷新</NButton>
        </NSpace>
      </template>

      <NSpace align="center" :size="12" style="margin-bottom: 12px">
        <NSelect
            v-model:value="runId"
            :options="runOptions"
            filterable
            placeholder="选择一次 run"
            style="width: 340px"
            @update:value="loadReport"
        />
        <NSelect
            v-model:value="annotatorFilter"
            :options="annotatorOptions"
            style="width: 160px"
            @update:value="loadReport"
        />
        <NButton
            v-if="runId"
            size="small"
            quaternary
            @click="router.push({ path: '/gold', query: { run: String(runId) } })"
        >
          去打分
        </NButton>
      </NSpace>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">{{ errorText }}</NAlert>

      <NAlert v-if="verdict" :type="verdict.type" :show-icon="false" style="margin-bottom: 12px">
        <NText style="font-size: 13px">{{ verdict.text }}</NText>
      </NAlert>

      <NGrid v-if="report" :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi span="4 s:2 m:1">
          <NStatistic
              label="幻觉判定 κ"
              :value="report.binary ? report.binary.kappa.toFixed(2) : '—'"
          />
          <NText depth="3" style="font-size: 12px">
            {{ report.binary ? kappaBand(report.binary.kappa).label : '没有可配对的判定' }}
          </NText>
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic
              label="一致率"
              :value="report.binary ? `${(report.binary.agreement * 100).toFixed(1)}%` : '—'"
          />
          <NText depth="3" style="font-size: 12px">
            样本 {{ report.binary?.pairs ?? 0 }} 题（人工"看不清"排除 {{ report.binary?.excluded_unclear ?? 0 }} 题）
          </NText>
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic label="金标覆盖率" :value="coverageText(report)" />
          <NText depth="3" style="font-size: 12px">
            有金标且有判定的 {{ report.gold_judged }} 题, 无判定的 {{ report.gold_unjudged }} 题
          </NText>
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic label="主标注员" :value="report.primary_annotator || '—'" />
          <NText depth="3" style="font-size: 12px">
            {{ report.annotators.length > 1 ? `共 ${report.annotators.length} 人` : '单人标注' }} ｜
            已复核 {{ report.gold_reviewed }}/{{ report.cases_with_gold }}
          </NText>
        </NGi>
      </NGrid>

      <NText v-if="report && !usable" depth="3" style="display: block; margin-top: 10px; font-size: 12px">
        结论尚未达标（样本 &lt; 20 题或覆盖率 &lt; 50%）: 上面的数字只能当方向参考, 不要用它给 judge 下判决。
      </NText>
    </NCard>

    <NCard v-if="report && report.binary" title="幻觉判定：混淆矩阵" style="margin-bottom: 16px">
      <NGrid :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi v-for="cell in confusions" :key="cell.key" span="4 s:2 m:1">
          <NCard size="small">
            <NStatistic :label="cell.label" :value="String(cell.count)" />
            <NTag size="tiny" :type="cell.type" style="margin-top: 6px">{{ cell.detail }}</NTag>
          </NCard>
        </NGi>
      </NGrid>
      <NText depth="3" style="display: block; margin-top: 10px; font-size: 12px">
        为什么还看 κ：幻觉率本身很低时, 一个"永远说没有幻觉"的判定也能拿到 90% 一致率,
        κ 会把"瞎猜也能对"的部分扣掉 —— κ≈0 就说明 judge 没比常数分类器多任何信息。
      </NText>
    </NCard>

    <NCard v-if="report" title="judge 判错的题（漏判在前）" style="margin-bottom: 16px">
      <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
        {{ disagreementSummary(disagreements) }}
      </NText>
      <NEmpty v-if="disagreements.length === 0" :description="disagreementEmptyHint(report)" />
      <NDataTable
          v-else
          :columns="disagreementColumns"
          :data="disagreements"
          :row-key="(row: CalibrationDisagreement) => String(row.case_id)"
          :pagination="false"
          :scroll-x="720"
          size="small"
      />
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        漏判会让幻觉被当成正确答出去, 误报只是多花人工 —— 改 judge prompt 时先修漏判。
      </NText>
    </NCard>

    <NCard
        v-for="section in scoreSections"
        :key="section.key"
        :title="`分数校准：${section.title}`"
        style="margin-bottom: 16px"
    >
      <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
        {{ section.hint }} ｜ {{ scoreLine(section.score) }}
      </NText>
      <NSpace align="center" style="margin-bottom: 10px">
        <NTag size="small">MAE {{ section.score.mae.toFixed(2) }}</NTag>
        <NTag size="small" :type="Math.abs(section.score.bias) < 0.05 ? 'default' : (section.score.bias > 0 ? 'warning' : 'info')">
          {{ biasText(section.score.bias) }}
        </NTag>
        <NText depth="3" style="font-size: 12px">
          人工均值 {{ section.score.human_mean.toFixed(2) }} ／ judge 均值 {{ section.score.judge_mean.toFixed(2) }}
        </NText>
      </NSpace>
      <NDataTable
          :columns="scoreColumns"
          :data="scoreRows(section.score)"
          :row-key="(row: { human: number }) => String(row.human)"
          :pagination="false"
          size="small"
      />
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        行 = 人工, 列 = judge, 加粗对角线 = 人机同分。颜色越深 = 落在这一格的题越多。
      </NText>
    </NCard>

    <NCard v-if="interText" title="人工之间的一致性（金标本身可信吗）" style="margin-bottom: 16px">
      <NText style="font-size: 13px">{{ interText }}</NText>
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        两人之间就不一致的题, 不应该用来评判 judge —— 先对齐标注口径, 再谈校准。
      </NText>
    </NCard>

    <NCard title="报告的自我提醒" style="margin-bottom: 16px">
      <NEmpty v-if="notes.length === 0" description="没有额外提醒（样本量与覆盖率都达标）" />
      <NSpace v-else vertical :size="8">
        <NAlert v-for="note in notes" :key="note.text" :type="note.type" :show-icon="false">
          <NText style="font-size: 12px">{{ note.text }}</NText>
        </NAlert>
      </NSpace>
    </NCard>
  </div>
</template>
