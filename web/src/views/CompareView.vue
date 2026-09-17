<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { TagType } from '../utils/format'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NGrid,
  NGi,
  NSelect,
  NSpace,
  NStatistic,
  NTag,
  NText,
} from 'naive-ui'
import { getRunCompare, listRuns } from '../api/runs'
import type { ABCaseDelta, ABReport, ABStratum, Run } from '../api/types'
import { formatPercent, shortHash, statusLabel, statusTagType } from '../utils/format'
import { flagLabel } from '../utils/report'
import {
  buildCompareCsv,
  buildCompareMarkdown,
  deltaTagType,
  exportFilename,
  formatDelta,
  flagTransitionText,
  isGenerationMetric,
  metricLabel,
  stratumFlagShift,
} from '../utils/compare'
import AsyncState from '../components/AsyncState.vue'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const runs = ref<Run[]>([])
const leftId = ref<number | null>(route.query.left ? Number(route.query.left) : null)
const rightId = ref<number | null>(route.query.right ? Number(route.query.right) : null)
const report = ref<ABReport | null>(null)
const loading = ref(false)
const loadingRuns = ref(false)
const errorText = ref('')

const runOptions = computed(() =>
  runs.value.map((run) => ({
    label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)} · ${statusLabel(run.status)}`,
    value: run.id,
  })),
)

/** 把 run 的关键信息也标在选项上方, 免得选错(选错是 A/B 最常见的误用)。 */
const leftRun = computed(() => runs.value.find((run) => run.id === leftId.value) ?? null)
const rightRun = computed(() => runs.value.find((run) => run.id === rightId.value) ?? null)

async function loadRuns() {
  loadingRuns.value = true
  try {
    runs.value = await listRuns({ limit: 200 })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loadingRuns.value = false
  }
}

async function runCompare() {
  if (!leftId.value || !rightId.value) {
    message.warning('请先选择左右两次 run')
    return
  }
  if (leftId.value === rightId.value) {
    message.warning('左右不能是同一次 run')
    return
  }
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getRunCompare(leftId.value, rightId.value)
    // 把选择写进 URL: 对比结果可以直接分享给同事
    router.replace({ query: { left: String(leftId.value), right: String(rightId.value) } })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
    report.value = null
  } finally {
    loading.value = false
  }
}

/** AsyncState 的重试: run 下拉的列表与对比结果都可能刚失败过, 两个一起重来才算真的重试。 */
async function reload() {
  await loadRuns()
  if (leftId.value && rightId.value) await runCompare()
}

onMounted(async () => {
  await loadRuns()
  if (leftId.value && rightId.value) await runCompare()
})

// ---- 展示 ----

const summaryRows = computed(() => {
  const source = report.value?.summary ?? {}
  return Object.entries(source).map(([metric, delta]) => ({
    metric,
    generation: isGenerationMetric(metric),
    ...delta,
  }))
})

const noiseFloor = computed(() => report.value?.noise_floor ?? 0)

function download(name: string, content: string, mime: string) {
  const blob = new Blob([content], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.click()
  URL.revokeObjectURL(url)
}

function exportCsv() {
  if (!report.value) return
  download(exportFilename(report.value.left, report.value.right, 'csv'),
      buildCompareCsv(report.value), 'text/csv')
}

function exportMarkdown() {
  if (!report.value) return
  download(exportFilename(report.value.left, report.value.right, 'md'),
      buildCompareMarkdown(report.value), 'text/markdown')
}

function exportJson() {
  if (!report.value) return
  download(exportFilename(report.value.left, report.value.right, 'json'),
      JSON.stringify(report.value, null, 2), 'application/json')
}

/** 方向感知的配色: 幻觉率下降要涂绿, 不能被当成"恶化"。 */
function metricTagType(row: { delta: number; higher_is_better: boolean }): TagType {
  return deltaTagType(row.delta, noiseFloor.value, row.higher_is_better !== false)
}

const summaryColumns: DataTableColumns<(typeof summaryRows.value)[number]> = [
  {
    title: '指标',
    key: 'metric',
    width: 170,
    render: (row) =>
        h(NSpace, { size: 4, align: 'center' }, {
          default: () => [
            h('span', {}, metricLabel(row.metric)),
            row.higher_is_better === false
                ? h(NTag, { size: 'tiny', bordered: false }, { default: () => '越低越好' })
                : null,
            row.generation ? h(NTag, { size: 'tiny', type: 'info', bordered: false }, { default: () => '生成侧' }) : null,
          ],
        }),
  },
  { title: '左(基准)', key: 'left', width: 110, render: (row) => formatPercent(row.left) },
  { title: '右(对照)', key: 'right', width: 110, render: (row) => formatPercent(row.right) },
  {
    title: '差值',
    key: 'delta',
    width: 120,
    render: (row) => h(NTag, { size: 'small', type: metricTagType(row) }, { default: () => formatDelta(row.delta) }),
  },
  { title: '改善', key: 'improved', width: 80 },
  { title: '恶化', key: 'worsened', width: 80 },
  {
    // 生成侧只在"两侧都有判定"的题上比 -> 样本量必须露出来
    title: '题数',
    key: 'cases',
    width: 80,
    render: (row) => h(NText, { depth: row.generation ? 2 : 3 }, { default: () => String(row.cases ?? 0) }),
  },
  { title: 'p 值', key: 'p_value', width: 100, render: (row) => (row.p_value ?? 0).toFixed(4) },
  {
    title: '结论',
    key: 'verdict',
    render: (row) =>
        row.below_noise
            ? h(NText, { depth: 3 }, { default: () => '落在噪声内, 无法区分' })
            : row.significant
                ? h(NTag, { size: 'small', type: metricTagType(row) }, { default: () => '显著' })
                : h(NText, { depth: 3 }, { default: () => '不显著' }),
  },
]

const transitionColumns: DataTableColumns<ABCaseDelta> = [
  { title: 'qid', key: 'qid', width: 110 },
  { title: '类别', key: 'category', width: 90, render: (row) => row.category || '—' },
  { title: '难度', key: 'difficulty', width: 70, render: (row) => row.difficulty || '—' },
  {
    title: '标签变化',
    key: 'transition',
    render: (row) => flagTransitionText(row, flagLabel),
  },
]

function stratumColumns(): DataTableColumns<ABStratum> {
  return [
    { title: '分组', key: 'key', width: 160 },
    { title: '题数', key: 'cases', width: 80 },
    {
      title: 'Recall 均值差',
      key: 'mean_delta',
      width: 130,
      render: (row) => h(NTag, { size: 'small', type: deltaTagType(row.mean_delta, noiseFloor.value) },
          { default: () => formatDelta(row.mean_delta) }),
    },
    { title: '改善', key: 'improved', width: 80 },
    { title: '恶化', key: 'worsened', width: 80 },
    { title: '有标签(左→右)', key: 'flags', render: (row) => stratumFlagShift(row) },
  ]
}
</script>

<template>
  <div>
    <NCard title="A/B 对比" style="margin-bottom: 16px">
      <template #header-extra>
        <NButton v-if="report" size="small" @click="router.push(`/runs/${report.right}`)">看右 run 报告</NButton>
      </template>

      <NSpace align="center" :size="12" style="margin-bottom: 12px">
        <NText depth="3">左(基准)</NText>
        <NSelect
            v-model:value="leftId"
            :options="runOptions"
            :loading="loadingRuns"
            filterable
            placeholder="选择基准 run"
            style="width: 300px"
        />
        <NText depth="3">右(对照)</NText>
        <NSelect
            v-model:value="rightId"
            :options="runOptions"
            :loading="loadingRuns"
            filterable
            placeholder="选择对照 run"
            style="width: 300px"
        />
        <NButton type="primary" :loading="loading" @click="runCompare">开始对比</NButton>
      </NSpace>

      <NSpace v-if="leftRun || rightRun" size="small">
        <NTag v-if="leftRun" size="small" :type="statusTagType(leftRun.status)">
          左 #{{ leftRun.id }} · dataset {{ leftRun.dataset_id }} · {{ shortHash(leftRun.config_hash, 12) }}
        </NTag>
        <NTag v-if="rightRun" size="small" :type="statusTagType(rightRun.status)">
          右 #{{ rightRun.id }} · dataset {{ rightRun.dataset_id }} · {{ shortHash(rightRun.config_hash, 12) }}
        </NTag>
      </NSpace>

      <NAlert v-if="errorText && report !== null" type="error" :show-icon="false" style="margin-top: 12px">{{ errorText }}</NAlert>
      <NAlert v-if="report && !report.comparable" type="warning" :show-icon="false" style="margin-top: 12px">
        不可比：{{ report.reason }}
      </NAlert>
      <NAlert v-if="report?.attribution_missing" type="warning" :show-icon="false" style="margin-top: 12px">
        {{ report.attribution_missing }}
      </NAlert>
      <NAlert v-if="report?.generation_note" type="info" :show-icon="false" style="margin-top: 12px">
        {{ report.generation_note }}
      </NAlert>
    </NCard>

    <!-- 主数据是一个对象(report): 三态判断用 report === null(对比结果没拿到), 而不是某个数组长度 -->
    <AsyncState
        :loading="loading && report === null"
        :error="report === null ? errorText : ''"
        :empty="!loading && report === null && !errorText"
        empty-text="先选左(基准)与右(对照)两次 run 并点「开始对比」，这里会给出指标差值与翻转题清单"
        @retry="reload"
    >
      <template v-if="report && report.comparable">
        <NCard title="指标对比" style="margin-bottom: 16px">
          <NGrid :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
            <NGi span="4 s:2 m:1">
              <NStatistic label="共同题目" :value="String(report.shared_cases)" />
            </NGi>
            <NGi span="4 s:2 m:1">
              <NStatistic label="修好的题" :value="String(report.fixed.length)" />
            </NGi>
            <NGi span="4 s:2 m:1">
              <NStatistic label="变坏的题" :value="String(report.broke.length)" />
            </NGi>
            <NGi span="4 s:2 m:1">
              <NStatistic label="噪声底" :value="String(report.noise_floor)" />
            </NGi>
          </NGrid>

          <NDataTable
              :columns="summaryColumns"
              :data="summaryRows"
              :row-key="(row) => row.metric"
              :scroll-x="700"
              size="small"
              style="margin-top: 12px"
          />
        </NCard>

        <NGrid :cols="2" :x-gap="16" :y-gap="16" responsive="screen" item-responsive style="margin-bottom: 16px">
          <NGi span="2 m:1">
            <NCard :title="`修好的题（${report.fixed.length}）`">
              <NText v-if="!report.fixed.length" depth="3">无</NText>
              <NDataTable
                  v-else
                  :columns="transitionColumns"
                  :data="report.fixed"
                  :row-key="(row: ABCaseDelta) => row.qid"
                  :scroll-x="420"
                  size="small"
              />
            </NCard>
          </NGi>
          <NGi span="2 m:1">
            <NCard :title="`变坏的题（${report.broke.length}）`">
              <NText v-if="!report.broke.length" depth="3">无</NText>
              <NDataTable
                  v-else
                  :columns="transitionColumns"
                  :data="report.broke"
                  :row-key="(row: ABCaseDelta) => row.qid"
                  :scroll-x="420"
                  size="small"
              />
            </NCard>
          </NGi>
        </NGrid>

        <NCard v-if="report.changed.length" :title="`标签变化（${report.changed.length}）· 不好不坏`" style="margin-bottom: 16px">
          <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
            这些题两侧都有问题, 只是问题种类变了（例如"漏召回"变成"排序靠后"）——
            它们不算修好也不算变坏, 单独列出来免得混进上面两栏。
          </NText>
          <NDataTable
              :columns="transitionColumns"
              :data="report.changed"
              :row-key="(row: ABCaseDelta) => row.qid"
              :scroll-x="420"
              size="small"
          />
        </NCard>

        <NCard title="分层结论" style="margin-bottom: 16px">
          <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
            同一份平均分可能掩盖方向相反的两种变化, 所以按维度拆开看(最差的排前面)。
          </NText>
          <NGrid :cols="2" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
            <NGi span="2 m:1">
              <NCard size="small" title="按归因标签">
                <NDataTable
                    :columns="stratumColumns()"
                    :data="report.by_flag"
                    :row-key="(row: ABStratum) => row.key"
                    :scroll-x="620"
                    size="small"
                />
              </NCard>
            </NGi>
            <NGi span="2 m:1">
              <NCard size="small" title="按类别">
                <NDataTable
                    :columns="stratumColumns()"
                    :data="report.by_category"
                    :row-key="(row: ABStratum) => row.key"
                    :scroll-x="620"
                    size="small"
                />
              </NCard>
            </NGi>
            <NGi span="2 m:1">
              <NCard size="small" title="按难度">
                <NDataTable
                    :columns="stratumColumns()"
                    :data="report.by_difficulty"
                    :row-key="(row: ABStratum) => row.key"
                    :scroll-x="620"
                    size="small"
                />
              </NCard>
            </NGi>
          </NGrid>
        </NCard>

        <NCard title="导出与怎么读" style="margin-bottom: 16px">
          <NSpace style="margin-bottom: 12px">
            <NButton size="small" @click="exportCsv">导出 CSV</NButton>
            <NButton size="small" @click="exportMarkdown">导出 Markdown</NButton>
            <NButton size="small" @click="exportJson">导出 JSON</NButton>
          </NSpace>
          <NText depth="3" style="display: block; font-size: 12px; line-height: 1.8">
            结论以「噪声底 + 翻转题清单」为准：差值小于 {{ report.noise_floor }}（跨 run 实测噪声底）就是**无法区分**，
            不要当成优化或回退。<br />
            p 值只作旁证 —— 检索指标离散且大量并列（recall 常只有 0 / 0.5 / 1），Wilcoxon 的正态近似在大量同分下并不可靠；<br />
            真正能拿去汇报的是"哪几道题从什么标签变成了什么"。
          </NText>
        </NCard>
      </template>
    </AsyncState>
  </div>
</template>
