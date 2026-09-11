<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
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
  NDrawerContent,
  NGi,
  NGrid,
  NList,
  NListItem,
  NProgress,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { getRunCaseResults, getRunProgress, getRunReport } from '../api/runs'
import type { RunCaseResult, RunProgress, RunReport } from '../api/types'
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

const route = useRoute()
const message = useMessage()
const runId = computed(() => Number(route.params.id))

const report = ref<RunReport | null>(null)
const progressInfo = ref<RunProgress | null>(null)
const caseResults = ref<RunCaseResult[]>([])
const loading = ref(false)
const errorText = ref('')
const flaggedOnly = ref(false)
const detail = ref<RunCaseResult | null>(null)
const showDetail = ref(false)
const autoRefresh = ref(true)

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

async function loadAll() {
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getRunReport(runId.value, 10)
    await Promise.all([loadCases(), loadProgress()])
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

function openDetail(row: RunCaseResult) {
  detail.value = row
  showDetail.value = true
}

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

const worstColumns: DataTableColumns<RunCaseResult> = [
  { title: 'qid', key: 'qid', width: 110 },
  { title: '问题', key: 'question', ellipsis: { tooltip: true } },
  {
    title: '难度',
    key: 'difficulty',
    width: 80,
    render: (row) =>
        row.difficulty
            ? h(NTag, { size: 'small', type: difficultyTagType(row.difficulty) }, { default: () => row.difficulty })
            : null,
  },
  { title: 'Recall', key: 'recall', width: 90, render: (row) => formatPercent(row.metrics?.recall) },
  {
    title: '首个命中位次',
    key: 'first_hit_rank',
    width: 120,
    render: (row) => (row.metrics?.first_hit_rank ? `第 ${row.metrics.first_hit_rank} 位` : '未命中'),
  },
  {
    title: '操作',
    key: 'actions',
    width: 110,
    render: (row) =>
        h(NButton, { size: 'small', quaternary: true, onClick: () => openDetail(row) }, { default: () => '检索明细' }),
  },
]

const caseColumns: DataTableColumns<RunCaseResult> = [
  { title: 'qid', key: 'qid', width: 110 },
  { title: '问题', key: 'question', ellipsis: { tooltip: true } },
  {
    title: '难度',
    key: 'difficulty',
    width: 80,
    render: (row) =>
        row.difficulty
            ? h(NTag, { size: 'small', type: difficultyTagType(row.difficulty) }, { default: () => row.difficulty })
            : null,
  },
  {
    title: '类别',
    key: 'category',
    width: 100,
    render: (row) => row.category || '—',
  },
  { title: 'Recall', key: 'recall', width: 90, render: (row) => formatPercent(row.metrics?.recall) },
  {
    title: 'MRR',
    key: 'rr',
    width: 80,
    render: (row) => formatScore(row.metrics?.reciprocal_rank),
  },
  {
    title: '标签',
    key: 'flags',
    width: 140,
    render: (row) =>
        row.flags && row.flags.length
            ? h(NSpace, { size: 4 }, {
              default: () => row.flags.map((flag) =>
                  h(NTag, { size: 'small', type: 'warning' }, { default: () => flag }),
              ),
            })
            : h(NText, { depth: 3 }, { default: () => '—' }),
  },
  {
    title: '操作',
    key: 'actions',
    width: 110,
    render: (row) =>
        h(NButton, { size: 'small', quaternary: true, onClick: () => openDetail(row) }, { default: () => '检索明细' }),
  },
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
    </NCard>

    <NCard :title="report ? `运行报告 · run #${report.run.id}` : '运行报告'" style="margin-bottom: 16px">
      <template #header-extra>
        <NTag :type="statusTagType(report?.run.status)">
          {{ statusLabel(report?.run.status) }}
        </NTag>
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

      <NSpace v-if="flagEntries.length" style="margin-top: 12px">
        <NText depth="3">归因标签统计：</NText>
        <NTag v-for="[flag, count] in flagEntries" :key="flag" size="small" type="warning">
          {{ flag }} × {{ count }}
        </NTag>
      </NSpace>
      <NText v-else depth="3" style="display: block; margin-top: 12px">
        暂无归因标签（retrieval_miss / hallucination 等在 M4 生成侧评测后写入）。
      </NText>
    </NCard>

    <NCard v-if="report" title="最差用例（按 Recall 升序）" style="margin-bottom: 16px">
      <NDataTable
          :columns="worstColumns"
          :data="report.worst_cases"
          :row-key="(row: RunCaseResult) => row.case_id"
          size="small"
      />
    </NCard>

    <NCard title="全部单题结果">
      <template #header-extra>
        <NSpace align="center">
          <NText depth="3">只看有标签的</NText>
          <NSwitch :value="flaggedOnly" @update:value="toggleFlagged" />
        </NSpace>
      </template>
      <NDataTable
          :columns="caseColumns"
          :data="caseResults"
          :loading="loading"
          :row-key="(row: RunCaseResult) => row.case_id"
          size="small"
      />
    </NCard>

    <NDrawer v-model:show="showDetail" :width="560">
      <NDrawerContent :title="detail ? `检索明细 · ${detail.qid}` : '检索明细'">
        <template v-if="detail">
          <NText style="display: block; margin-bottom: 8px">{{ detail.question }}</NText>
          <NSpace size="small" style="margin-bottom: 12px">
            <NTag size="small">Recall {{ formatPercent(detail.metrics?.recall) }}</NTag>
            <NTag size="small">gold 数 {{ detail.metrics?.gold_count ?? '—' }}</NTag>
            <NTag size="small">命中 {{ detail.metrics?.hits ?? '—' }}</NTag>
            <NTag size="small">
              首个命中 {{ detail.metrics?.first_hit_rank ? `第 ${detail.metrics.first_hit_rank} 位` : '未命中' }}
            </NTag>
          </NSpace>

          <NText depth="3" style="display: block; margin-bottom: 6px">
            检索到的 chunk（按相似度降序）：
          </NText>
          <NList bordered size="small">
            <NListItem v-for="(item, index) in detail.retrieved" :key="item.point_id">
              <NSpace align="center">
                <NTag size="small" :bordered="false">#{{ index + 1 }}</NTag>
                <NText code>{{ item.doc_id || '—' }}</NText>
                <NText depth="3">score {{ formatScore(item.score) }}</NText>
              </NSpace>
            </NListItem>
          </NList>
          <NText depth="3" style="display: block; margin-top: 10px">
            chunk 正文与幻觉证据将在 M4 生成侧评测接入后补充展示。
          </NText>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>