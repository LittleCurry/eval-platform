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
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDropdown,
  NDrawerContent,
  NGi,
  NGrid,
  NList,
  NListItem,
  NProgress,
  NSelect,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { getCaseContext, getRunCaseResults, getRunProgress, getRunReport, listRuns } from '../api/runs'
import type { CaseContextResponse, Run, RunCaseResult, RunProgress, RunReport } from '../api/types'
import {
  difficultyTagType,
  formatDateTime,
  formatPercent,
  formatProgress,
  formatScore,
  progressPercent,
  shortHash,
  statusLabel,
  statusTagType,
} from '../utils/format'
import {
  answerSnippet,
  buildReportCsv,
  buildReportMarkdown,
  reportExportFilename,
  attributionText,
  claimLabel,
  claimCounter,
  claimSummary,
  claimTagType,
  contextNotice,
  fallbackContextRows,
  flagLabel,
  flagTagType,
  generationSummary,
  hasLlmMetrics,
  missingCaseNotice,
  primaryFlag,
  ratesConsistent,
  rubricSummary,
} from '../utils/report'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const runId = computed(() => Number(route.params.id))
// M5-1: "和哪次 run 对比" —— 同评测集的 run 优先, 免得选到不可比的组合
const allRuns = ref<Run[]>([])
const compareTarget = ref<number | null>(null)
// M7-3: 导出下拉(CSV 给"拿数据的人", Markdown 给"贴文档/群里的人")
const exportOptions = [
  { label: '导出 CSV（含逐题明细）', key: 'csv' },
  { label: '导出 Markdown（贴文档/群里）', key: 'md' },
]

/**
 * 下载用 Blob + a[download], 不经过服务端 —— 报告接口已经返回结构化结果,
 * 导出只是它的另一种视图(D19: 计算在 Go, 格式化在前端, 不写第二遍口径)。
 */
function download(name: string, content: string, mime: string) {
  const blob = new Blob([content], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

function onExport(key: string) {
  if (!report.value) return
  if (key === 'csv') {
    download(reportExportFilename(report.value.run.id, 'csv'), buildReportCsv(report.value), 'text/csv')
    message.success('已导出 CSV')
    return
  }
  if (key === 'md') {
    download(reportExportFilename(report.value.run.id, 'md'), buildReportMarkdown(report.value), 'text/markdown')
    message.success('已导出 Markdown')
  }
}

const report = ref<RunReport | null>(null)
const progressInfo = ref<RunProgress | null>(null)
const caseResults = ref<RunCaseResult[]>([])
const loading = ref(false)
const errorText = ref('')
const flaggedOnly = ref(false)
const flagFilter = ref<string | null>(null)
const detail = ref<RunCaseResult | null>(null)
const showDetail = ref(false)
const autoRefresh = ref(true)
// M4-4.1: 抽屉里那块"检索上下文正文"(打开时按需从向量库取)
const context = ref<CaseContextResponse | null>(null)
const contextLoading = ref(false)
const contextErrorText = ref('')
let contextSeq = 0

const POLL_INTERVAL_MS = 3000
let timer: number | undefined

const isActive = computed(() => {
  const status = progressInfo.value?.status ?? report.value?.run.status
  return status === 'pending' || status === 'running'
})

async function loadCases() {
  caseResults.value = await getRunCaseResults(runId.value, { limit: 200, flaggedOnly: flaggedOnly.value })
}

async function loadProgress() {
  progressInfo.value = await getRunProgress(runId.value)
}

async function loadRunsForCompare() {
  try {
    allRuns.value = await listRuns({ limit: 200 })
  } catch {
    // 对比入口是锦上添花: 拉不到 run 列表就不显示下拉, 不影响报告主体
  }
}

/** 对比候选: 排除自己; 同评测集的排前面(跨集会直接判不可比, 先帮用户避开)。 */
const compareOptions = computed(() => {
  const self = report.value?.run
  const others = allRuns.value.filter((run) => run.id !== runId.value)
  const sameDataset = others.filter((run) => !self || run.dataset_id === self.dataset_id)
  const rest = others.filter((run) => self && run.dataset_id !== self.dataset_id)
  return [
    ...sameDataset.map((run) => ({
      label: `#${run.id} · 同集 · ${shortHash(run.config_hash)} · ${statusLabel(run.status)}`,
      value: run.id,
    })),
    ...rest.map((run) => ({
      label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)}`,
      value: run.id,
    })),
  ]
})

function openCompare(target: number | null) {
  if (!target) return
  // 当前 run 作为基准(左), 选中的作为对照(右)
  router.push({ path: '/compare', query: { left: String(runId.value), right: String(target) } })
}

async function loadAll() {
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getRunReport(runId.value, 10)
    await Promise.all([loadCases(), loadProgress(), loadRunsForCompare()])
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await loadAll()
  timer = window.setInterval(async () => {
    if (!autoRefresh.value || !isActive.value) return
    try {
      await loadProgress()
      // 完成后补一次全量(指标/明细随之更新)
      if (!isActive.value) report.value = await getRunReport(runId.value, 10)
    } catch {
      // 轮询失败不打断页面, 等下一轮
    }
  }, POLL_INTERVAL_MS)
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})

async function toggleFlagged(value: boolean) {
  flaggedOnly.value = value
  try {
    await loadCases()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

/**
 * 打开抽屉并**按需取回 chunk 正文**(M4-4.1)。
 *
 * 正文只在向量库里有一份、不落库(D17), 所以只能打开时现取; 用自增序号挡住竞态:
 * 快速点开另一题时, 先发出的请求可能后回来, 不挡的话会把正文显示成上一题的。
 */
async function openDetail(row: RunCaseResult) {
  detail.value = row
  showDetail.value = true
  context.value = null
  contextErrorText.value = ''
  contextLoading.value = true
  const seq = ++contextSeq
  try {
    const response = await getCaseContext(runId.value, row.case_id)
    if (seq !== contextSeq) return
    context.value = response
  } catch (err) {
    if (seq !== contextSeq) return
    contextErrorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (seq === contextSeq) contextLoading.value = false
  }
}

/** 点标签统计即按该标签筛选(数据已在本地 200 条内, 不必再打接口)。 */
function toggleFlagFilter(flag: string) {
  flagFilter.value = flagFilter.value === flag ? null : flag
}

const filteredCases = computed(() => {
  const flag = flagFilter.value
  if (!flag) return caseResults.value
  return caseResults.value.filter((row) => (row.flags ?? []).includes(flag))
})

const detailClaims = computed(() => detail.value?.judge?.claims ?? [])
const detailPrimaryFlag = computed(() => primaryFlag(detail.value?.flags))
const detailGeneration = computed(() => generationSummary(detail.value?.generation))

// ---- 检索上下文正文(M4-4.1) ----

/**
 * 抽屉里要显示的 chunk 行: 优先用向量库取回的正文;
 * 取不到(旧后端/降级/断网)时回落到 retrieved 的骨架, 至少还能看到"检索了什么"。
 */
const detailContextRows = computed(() => {
  const chunks = context.value?.chunks
  if (chunks && chunks.length > 0) {
    return chunks.map((chunk) => ({
      point_id: chunk.point_id,
      doc_id: chunk.doc_id ?? '',
      score: chunk.score,
      section: chunk.section ?? '',
      text: chunk.text ?? '',
      found: chunk.found,
    }))
  }
  return fallbackContextRows(detail.value?.retrieved)
})

/** 降级提示: 网络层错误优先, 其次是后端给的 error / 缺失统计。 */
const detailContextNotice = computed(() => {
  if (contextErrorText.value) return `chunk 正文暂不可用：${contextErrorText.value}`
  return contextNotice(context.value)
})

// ---- 生成侧 / 判定侧指标(M4-4) ----

const llmCardVisible = computed(() => hasLlmMetrics(report.value?.metrics))

const llmStats = computed<{ label: string; value: string }[]>(() => {
  const m = report.value?.metrics
  if (!m) return []
  return [
    { label: '答案生成数', value: String(m.answers_generated ?? 0) },
    { label: '断言支持率', value: formatPercent(m.claim_support_rate) },
    { label: '幻觉率', value: formatPercent(m.hallucination_rate) },
    { label: '无关断言率', value: formatPercent(m.irrelevant_rate) },
    { label: '断言总数', value: String(m.claims_total ?? 0) },
    { label: '平均断言/答案', value: formatScore(m.avg_claims_per_answer, 2) },
    { label: 'relevance 均值', value: formatScore(m.relevance_avg, 2) },
    { label: 'helpfulness 均值', value: formatScore(m.helpfulness_avg, 2) },
    { label: '判定题数', value: String(m.cases_judged ?? 0) },
    { label: 'judge token', value: String((m.judge_prompt_tokens ?? 0) + (m.judge_completion_tokens ?? 0)) },
    { label: '缓存命中', value: String(m.judge_cache_hits ?? 0) },
    { label: '生成 token', value: String((m.prompt_tokens ?? 0) + (m.completion_tokens ?? 0)) },
  ]
})

/** 三率之和必须为 1; 不成立说明口径出了问题, 界面必须自己喊出来。 */
const ratesInconsistent = computed(() => ratesConsistent(report.value?.metrics) === false)

const attributionLine = computed(() => attributionText(report.value?.metrics))

const missingNotice = computed(() => missingCaseNotice(progressInfo.value?.job?.progress.failed))

// ---- 配置快照展示(把 jsonb 拆成可读文本) ----

function snapshotSection(key: string): Record<string, unknown> {
  const snapshot = report.value?.run.config_snapshot
  if (!snapshot) return {}
  const section = snapshot[key]
  return section && typeof section === 'object' ? (section as Record<string, unknown>) : {}
}

const chunkingText = computed(() => {
  const c = snapshotSection('chunking')
  return `${c.strategy ?? '—'} · chunk_size=${c.chunk_size ?? '—'} · overlap=${c.overlap ?? '—'} · min_chars=${c.min_chars ?? '—'}`
})

const retrievalText = computed(() => {
  const r = snapshotSection('retrieval')
  const reranker = (r.reranker ?? {}) as Record<string, unknown>
  return `top_k=${r.top_k ?? '—'} · reranker=${reranker.enabled ? '开启' : '关闭'}`
})

const embeddingText = computed(() => {
  const e = snapshotSection('embedding')
  return `${e.provider ?? '—'} / ${e.model ?? '—'} · dim=${e.dim ?? '—'}`
})

const flagEntries = computed(() => Object.entries(report.value?.flag_counts ?? {}))

const progressDone = computed(() => {
  const p = progressInfo.value?.job?.progress
  if (!p) return 0
  return p.succeeded + p.failed
})

const progressTotal = computed(() => progressInfo.value?.job?.progress.total ?? 0)

const progressLabel = computed(() => {
  const p = progressInfo.value?.job?.progress
  if (!p) return '无队列任务'
  const parts = [`成功 ${p.succeeded}`, `失败 ${p.failed}`]
  if (p.running) parts.push(`进行中 ${p.running}`)
  if (p.pending) parts.push(`待跑 ${p.pending}`)
  return parts.join(' · ')
})

// ---- 表格列 ----

/** 主因标签(flags[0]): 报告的第一眼应该是"这题的锅在谁头上"。 */
function primaryFlagCell(row: RunCaseResult) {
  const flag = primaryFlag(row.flags)
  if (!flag) return h(NText, { depth: 3 }, { default: () => '—' })
  return h('span', { title: (row.flags ?? []).join(' → ') },
      [h(NTag, { size: 'small', type: flagTagType(flag) }, { default: () => flagLabel(flag) })])
}

function rubricCell(row: RunCaseResult) {
  const summary = rubricSummary(row.judge)
  if (summary === null) return h(NText, { depth: 3 }, { default: () => '—' })
  const reason = row.judge?.rubric?.reason ?? ''
  return h('span', { title: reason }, [summary])
}

/**
 * 断言列用紧凑计数 "支持/无据/无关"(列宽只有 96px, 长文案会被裁)。
 * 出现"无据/无关"时标红 —— 一行坏答案在表里第一眼就该被看见。
 */
function claimsCell(row: RunCaseResult) {
  const counts = claimCounter(row.judge)
  if (counts === null) return h(NText, { depth: 3 }, { default: () => '—' })
  const title = `${counts.supported} 有据 / ${counts.unsupported} 无据 / ${counts.irrelevant} 无关`
  const text = `${counts.supported}/${counts.unsupported}/${counts.irrelevant}`
  if (counts.unsupported > 0 || counts.irrelevant > 0) {
    return h('span', { title, style: 'color: #d03050; font-weight: 600' }, [text])
  }
  return h('span', { title, style: 'color: rgba(127, 127, 127, 0.9)' }, [text])
}

function answerCell(row: RunCaseResult) {
  // 不截断(交给列宽省略 + 悬浮 tooltip), 只做单行化, 免得 tooltip 也只剩半句
  return answerSnippet(row.answer, 0) ?? h(NText, { depth: 3 }, { default: () => '—' })
}

// 列宽合计 = 固定列 + 问题列(自适应)。容器可用宽度约 1110px(1200 上限 - 两侧留白),
// 所以刻意把固定列压在 ~890px, 留 200px 以上给"问题"; 同时给表格设 scroll-x,
// 窗口更窄时改为横向滚动, 而不是把右边的列裁掉。
const CASE_TABLE_SCROLL_X = 1086
const WORST_TABLE_SCROLL_X = 776

const qidColumn: DataTableColumns<RunCaseResult>[number] = {
  // 固定在左侧: 横向滚动时也知道自己在看哪一题
  title: 'qid',
  key: 'qid',
  width: 96,
  fixed: 'left',
}

const difficultyColumn: DataTableColumns<RunCaseResult>[number] = {
  title: '难度',
  key: 'difficulty',
  width: 64,
  render: (row) =>
      row.difficulty
          ? h(NTag, { size: 'small', type: difficultyTagType(row.difficulty) }, { default: () => row.difficulty })
          : null,
}

const actionColumn: DataTableColumns<RunCaseResult>[number] = {
  title: '操作',
  key: 'actions',
  width: 92,
  fixed: 'right',
  render: (row) =>
      h(NButton, { size: 'small', quaternary: true, onClick: () => openDetail(row) }, { default: () => '详情' }),
}

const worstColumns: DataTableColumns<RunCaseResult> = [
  qidColumn,
  { title: '问题', key: 'question', ellipsis: { tooltip: true } },
  difficultyColumn,
  { title: 'Recall', key: 'recall', width: 82, render: (row) => formatPercent(row.metrics?.recall) },
  {
    title: '首个命中',
    key: 'first_hit_rank',
    width: 110,
    render: (row) => (row.metrics?.first_hit_rank ? `第 ${row.metrics.first_hit_rank} 位` : '未命中'),
  },
  { title: '主因', key: 'primary_flag', width: 132, render: (row) => primaryFlagCell(row) },
  actionColumn,
]

const caseColumns: DataTableColumns<RunCaseResult> = [
  qidColumn,
  { title: '问题', key: 'question', ellipsis: { tooltip: true } },
  difficultyColumn,
  { title: 'Recall', key: 'recall', width: 82, render: (row) => formatPercent(row.metrics?.recall) },
  {
    title: 'MRR',
    key: 'rr',
    width: 72,
    render: (row) => formatScore(row.metrics?.reciprocal_rank),
  },
  { title: '主因', key: 'primary_flag', width: 132, render: (row) => primaryFlagCell(row) },
  { title: '断言', key: 'claims', width: 96, render: (row) => claimsCell(row) },
  { title: 'rubric', key: 'rubric', width: 72, render: (row) => rubricCell(row) },
  {
    title: '答案',
    key: 'answer',
    width: 180,
    // 交给表格做省略 + 悬浮显示全文: 单元格里再截断一次会让 tooltip 也只剩半句
    ellipsis: { tooltip: true },
    render: (row) => answerCell(row),
  },
  actionColumn,
]
</script>

<template>
  <div>
    <NCard title="任务进度" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NTag v-if="isActive && autoRefresh" size="small" type="info">自动刷新中</NTag>
          <NSpace align="center" :size="4">
            <NText depth="3" style="font-size: 12px">自动刷新</NText>
            <NSwitch v-model:value="autoRefresh" size="small" />
          </NSpace>
          <NButton size="small" :loading="loading" @click="loadAll">刷新</NButton>
        </NSpace>
      </template>

      <NProgress
          type="line"
          :percentage="progressPercent(progressDone, progressTotal)"
          :height="10"
          :status="progressInfo?.job && progressInfo.job.progress.failed > 0 ? 'error' : 'default'"
      />
      <NSpace align="center" style="margin-top: 10px">
        <NText>
          {{ formatProgress(progressDone, progressTotal) }} · {{ progressLabel }}
        </NText>
        <NTag v-if="progressInfo?.stale" size="small" type="error">疑似 worker 掉线, 可触发接管</NTag>
        <NText depth="3" style="font-size: 12px">
          最近心跳: {{ formatDateTime(progressInfo?.job?.heartbeat_at) }}
        </NText>
      </NSpace>

      <NAlert v-if="missingNotice" type="warning" :show-icon="false" style="margin-top: 10px">
        {{ missingNotice }}
      </NAlert>
    </NCard>

    <NCard :title="report ? `运行报告 · run #${report.run.id}` : '运行报告'" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center" :size="8">
          <NSelect
              v-if="compareOptions.length"
              :value="compareTarget"
              :options="compareOptions"
              filterable
              size="small"
              placeholder="和哪次 run 对比？"
              style="width: 260px"
              @update:value="openCompare"
          />
          <NDropdown v-if="report" :options="exportOptions" @select="onExport">
            <NButton size="small" quaternary>导出报告</NButton>
          </NDropdown>
          <NButton
              v-if="flagEntries.length"
              size="small"
              quaternary
              @click="router.push({ path: '/annotations', query: { run: String(runId) } })"
          >
            去标注这 {{ flagEntries.reduce((sum, [, count]) => sum + count, 0) }} 条标签
          </NButton>
          <!-- M6-3: 只有真判过的 run 才谈得上校准 judge -->
          <NButton
              v-if="Number(report?.metrics?.cases_judged ?? 0) > 0"
              size="small"
              quaternary
              @click="router.push({ path: '/calibration', query: { run: String(runId) } })"
          >
            校准 judge（{{ report?.metrics?.cases_judged }} 题有判定）
          </NButton>
          <!-- M6-4: 闭环的基线就是当前这次 run(标注挂在它上面) -->
          <NButton
              size="small"
              quaternary
              @click="router.push({ path: '/closure', query: { baseline: String(runId) } })"
          >
            看标注闭环
          </NButton>
          <NTag :type="statusTagType(report?.run.status)">
            {{ statusLabel(report?.run.status) }}
          </NTag>
        </NSpace>
      </template>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
        {{ errorText }}
      </NAlert>

      <NGrid v-if="report" :cols="6" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi span="6 s:3 m:2">
          <NStatistic label="Recall@k" :value="formatPercent(report.metrics?.recall_at_k)" />
        </NGi>
        <NGi span="6 s:3 m:2">
          <NStatistic label="Precision@k" :value="formatPercent(report.metrics?.precision_at_k)" />
        </NGi>
        <NGi span="6 s:3 m:2">
          <NStatistic label="MRR@k" :value="formatPercent(report.metrics?.mrr_at_k)" />
        </NGi>
        <NGi span="6 s:3 m:2">
          <NStatistic label="Hit@k" :value="formatPercent(report.metrics?.hit_at_k)" />
        </NGi>
        <NGi span="6 s:3 m:2">
          <NStatistic label="可评测题数" :value="String(report.metrics?.cases_evaluated ?? '—')" />
        </NGi>
        <NGi span="6 s:3 m:2">
          <NStatistic label="top-k" :value="String(report.metrics?.k ?? '—')" />
        </NGi>
      </NGrid>
    </NCard>

    <NCard v-if="llmCardVisible" title="生成与判定（M4 生成侧评测）" style="margin-bottom: 16px">
      <NAlert v-if="ratesInconsistent" type="error" :show-icon="false" style="margin-bottom: 12px">
        三率之和不为 1（支持率 + 幻觉率 + 无关率）—— 口径本身出了问题，先别拿这份报告下结论。
      </NAlert>

      <NGrid :cols="6" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi v-for="stat in llmStats" :key="stat.label" span="6 s:3 m:2">
          <NStatistic :label="stat.label" :value="stat.value" />
        </NGi>
      </NGrid>

      <NText v-if="attributionLine" depth="3" style="display: block; margin-top: 12px; font-size: 12px">
        {{ attributionLine }}
      </NText>
    </NCard>

    <NCard v-if="report" title="实验配置（可复现四件套）" style="margin-bottom: 16px">
      <NDescriptions :column="2" label-placement="left" bordered size="small">
        <NDescriptionsItem label="数据集 ID">{{ report.run.dataset_id }}</NDescriptionsItem>
        <NDescriptionsItem label="语料库 ID">{{ report.run.corpus_id ?? '—' }}</NDescriptionsItem>
        <NDescriptionsItem label="代码版本 (git_sha)">{{ report.run.git_sha || '—' }}</NDescriptionsItem>
        <NDescriptionsItem label="配置指纹 (config_hash)">
          {{ shortHash(report.run.config_hash, 16) }}…
        </NDescriptionsItem>
        <NDescriptionsItem label="切分配置">{{ chunkingText }}</NDescriptionsItem>
        <NDescriptionsItem label="检索配置">{{ retrievalText }}</NDescriptionsItem>
        <NDescriptionsItem label="向量模型">{{ embeddingText }}</NDescriptionsItem>
        <NDescriptionsItem label="起止时间">
          {{ formatDateTime(report.run.started_at) }} ~ {{ formatDateTime(report.run.finished_at) }}
        </NDescriptionsItem>
      </NDescriptions>

      <NSpace v-if="flagEntries.length" align="center" style="margin-top: 12px">
        <NText depth="3">归因标签统计（点一下筛选）：</NText>
        <NTag
            v-for="[flag, count] in flagEntries"
            :key="flag"
            size="small"
            :type="flagTagType(flag)"
            :bordered="flagFilter !== flag"
            style="cursor: pointer"
            :title="flag"
            @click="toggleFlagFilter(flag)"
        >
          {{ flagLabel(flag) }} × {{ count }}
        </NTag>
        <NButton v-if="flagFilter" size="tiny" quaternary @click="toggleFlagFilter(flagFilter)">
          清除筛选
        </NButton>
      </NSpace>
      <NText v-else depth="3" style="display: block; margin-top: 12px">
        本次 run 没有任何归因标签（检索与判定都没发现环节缺陷）。
      </NText>
    </NCard>

    <NCard v-if="report" title="最差用例（按 Recall 升序）" style="margin-bottom: 16px">
      <NDataTable
          :columns="worstColumns"
          :data="report.worst_cases"
          :row-key="(row: RunCaseResult) => row.case_id"
          :scroll-x="WORST_TABLE_SCROLL_X"
          size="small"
      />
    </NCard>

    <NCard title="全部单题结果">
      <template #header-extra>
        <NSpace align="center">
          <NTag v-if="flagFilter" size="small" type="warning">筛选中：{{ flagLabel(flagFilter) }}</NTag>
          <NText depth="3">只看有标签的</NText>
          <NSwitch :value="flaggedOnly" @update:value="toggleFlagged" />
        </NSpace>
      </template>
      <NDataTable
          :columns="caseColumns"
          :data="filteredCases"
          :loading="loading"
          :row-key="(row: RunCaseResult) => row.case_id"
          :scroll-x="CASE_TABLE_SCROLL_X"
          size="small"
      />
    </NCard>

    <NDrawer v-model:show="showDetail" :width="620">
      <NDrawerContent :title="detail ? `case 详情 · ${detail.qid}` : 'case 详情'">
        <template v-if="detail">
          <NSpace align="center" style="margin-bottom: 8px">
            <NTag v-if="detailPrimaryFlag" size="small" :type="flagTagType(detailPrimaryFlag)">
              主因：{{ flagLabel(detailPrimaryFlag) }}
            </NTag>
            <NText v-else depth="3">无归因标签</NText>
            <NTag v-if="detail.flags && detail.flags.length > 1" size="small" :bordered="false">
              还有 {{ detail.flags.length - 1 }} 个标签
            </NTag>
          </NSpace>

          <NText style="display: block; margin-bottom: 8px">{{ detail.question }}</NText>

          <NSpace size="small" style="margin-bottom: 12px">
            <NTag v-if="detail.category" size="small" :bordered="false">{{ detail.category }}</NTag>
            <NTag
                v-if="detail.difficulty"
                size="small"
                :type="difficultyTagType(detail.difficulty)"
            >
              {{ detail.difficulty }}
            </NTag>
            <NTag size="small">Recall {{ formatPercent(detail.metrics?.recall) }}</NTag>
            <NTag size="small">gold 数 {{ detail.metrics?.gold_count ?? '—' }}</NTag>
            <NTag size="small">命中 {{ detail.metrics?.hits ?? '—' }}</NTag>
            <NTag size="small">
              首个命中 {{ detail.metrics?.first_hit_rank ? `第 ${detail.metrics.first_hit_rank} 位` : '未命中' }}
            </NTag>
          </NSpace>

          <NCard v-if="detail.answer" size="small" title="答案" style="margin-bottom: 12px">
            <div class="answer-block">{{ detail.answer }}</div>
            <NText v-if="detailGeneration" depth="3" style="display: block; margin-top: 6px; font-size: 12px">
              {{ detailGeneration }}
            </NText>
          </NCard>

          <NCard v-if="detailClaims.length" size="small" title="断言逐条核对" style="margin-bottom: 12px">
            <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
              {{ claimSummary(detail.judge) }} —— 红行就是"资料里找不到依据"的句子（幻觉）。
            </NText>
            <NList bordered size="small">
              <NListItem v-for="claim in detailClaims" :key="claim.id">
                <div class="claim-row" :class="`claim-${claim.label}`">
                  <NSpace align="center" :size="6" style="margin-bottom: 4px">
                    <NTag size="tiny" :type="claimTagType(claim.label)">{{ claimLabel(claim.label) }}</NTag>
                    <NText>{{ claim.text }}</NText>
                  </NSpace>
                  <NText v-if="claim.evidence" depth="3" style="display: block; font-size: 12px">
                    依据：{{ claim.evidence }}
                  </NText>
                  <NText v-else depth="3" style="display: block; font-size: 12px">
                    依据：无（检索到的资料里找不到支持这句话的内容）
                  </NText>
                  <NText v-if="claim.reason" depth="3" style="display: block; font-size: 12px">
                    判定理由：{{ claim.reason }}
                  </NText>
                </div>
              </NListItem>
            </NList>
          </NCard>

          <NCard v-if="detail.judge?.rubric" size="small" title="rubric 打分" style="margin-bottom: 12px">
            <NSpace size="small" style="margin-bottom: 6px">
              <NTag size="small" :type="detail.judge.rubric.relevance <= 3 ? 'error' : 'success'">
                relevance {{ detail.judge.rubric.relevance }}
              </NTag>
              <NTag size="small" :type="detail.judge.rubric.helpfulness <= 3 ? 'error' : 'success'">
                helpfulness {{ detail.judge.rubric.helpfulness }}
              </NTag>
            </NSpace>
            <NText v-if="detail.judge.rubric.reason" depth="3" style="font-size: 12px">
              {{ detail.judge.rubric.reason }}
            </NText>
          </NCard>

          <NText depth="3" style="display: block; margin-bottom: 6px">
            检索到的 chunk（按相似度降序，正文按需从向量库取）：
          </NText>
          <NAlert v-if="contextLoading" type="info" :show-icon="false" style="margin-bottom: 8px">
            正在取回 chunk 正文…
          </NAlert>
          <NAlert v-else-if="detailContextNotice" type="warning" :show-icon="false" style="margin-bottom: 8px">
            {{ detailContextNotice }}
          </NAlert>
          <NList bordered size="small">
            <NListItem v-for="(item, index) in detailContextRows" :key="item.point_id">
              <div class="chunk-row" :class="{ 'chunk-row-missing': !item.found }">
                <NSpace align="center" :size="6" style="margin-bottom: 4px">
                  <NTag size="small" :bordered="false">#{{ index + 1 }}</NTag>
                  <NText code>{{ item.doc_id || '—' }}</NText>
                  <NText depth="3">score {{ formatScore(item.score) }}</NText>
                  <NTag v-if="detail.metrics?.first_hit_rank === index + 1" size="tiny" type="success">
                    首命中（gold）
                  </NTag>
                </NSpace>
                <NText v-if="item.section" depth="3" style="display: block; font-size: 12px">
                  {{ item.section }}
                </NText>
                <div v-if="item.found && item.text" class="chunk-text">{{ item.text }}</div>
                <NText v-else depth="3" style="display: block; font-size: 12px">
                  正文缺失（该 point 不在当前集合里，索引可能被重建过）
                </NText>
              </div>
            </NListItem>
          </NList>
          <NText depth="3" style="display: block; margin-top: 10px; font-size: 12px">
            正文不落库、按需从向量库取（见 process.md D17）；集合名由语料库 + 切分指纹推导，
            所以历史 run 也能看到当时那份上下文。
          </NText>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.answer-block {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
  padding: 8px 10px;
  border-radius: 8px;
  background: rgba(127, 127, 127, 0.08);
}

.claim-row {
  border-left: 4px solid transparent;
  padding: 6px 8px;
  border-radius: 6px;
}

.claim-supported {
  border-left-color: #18a058;
  background: rgba(24, 160, 88, 0.08);
}

.claim-unsupported {
  border-left-color: #d03050;
  background: rgba(208, 48, 80, 0.1);
}

.claim-irrelevant {
  border-left-color: #f0a020;
  background: rgba(240, 160, 32, 0.1);
}

/* M4-4.1: 上下文正文块。限高 + 内部滚动 —— 抽屉本身不该被一整篇 chunk 撑爆。 */
.chunk-row {
  width: 100%;
}

.chunk-row-missing {
  opacity: 0.75;
}

.chunk-text {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.55;
  font-size: 12px;
  max-height: 220px;
  overflow: auto;
  margin-top: 4px;
  padding: 6px 8px;
  border-radius: 6px;
  background: rgba(127, 127, 127, 0.08);
}
</style>