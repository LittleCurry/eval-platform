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
  NInputNumber,
  NProgress,
  NSpace,
  NSwitch,
  NTag,
  NText,
  NTooltip,
} from 'naive-ui'
import { getRunProgress, listRuns } from '../api/runs'
import type { Run, RunProgress } from '../api/types'
import {
  formatDateTime,
  formatPercent,
  formatProgress,
  progressPercent,
  shortHash,
  statusLabel,
  statusTagType,
} from '../utils/format'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const runs = ref<Run[]>([])
const progressMap = ref<Record<number, RunProgress>>({})
const loading = ref(false)
const errorText = ref('')
const autoRefresh = ref(true)
const datasetFilter = ref<number | null>(
    route.query.dataset_id ? Number(route.query.dataset_id) : null,
)

const hasActiveRun = computed(() =>
    runs.value.some((run) => run.status === 'pending' || run.status === 'running'),
)

async function loadProgressOnly() {
  const active = runs.value.filter((run) => run.status === 'pending' || run.status === 'running')
  if (!active.length) {
    progressMap.value = {}
    return
  }
  const entries = await Promise.all(
      active.map(async (run) => [run.id, await getRunProgress(run.id)] as const),
  )
  progressMap.value = Object.fromEntries(entries)
}

async function load(withProgress = true) {
  loading.value = true
  errorText.value = ''
  try {
    runs.value = await listRuns({ datasetId: datasetFilter.value ?? undefined, limit: 50 })
    if (withProgress) await loadProgressOnly()
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function refresh() {
  await load()
  message.success(`已加载 ${runs.value.length} 条运行记录`)
}

// 有活跃任务时每 3 秒轮询一次(空闲则不打扰后端)
const POLL_INTERVAL_MS = 3000
let timer: number | undefined

onMounted(async () => {
  await load()
  timer = window.setInterval(async () => {
    if (!autoRefresh.value || !hasActiveRun.value) return
    await load()
  }, POLL_INTERVAL_MS)
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})

function renderProgress(row: Run) {
  const snapshot = progressMap.value[row.id]
  if (!snapshot || !snapshot.job) {
    return h(NText, { depth: 3 }, { default: () => (row.status === 'succeeded' ? '—' : '无队列任务') })
  }
  const job = snapshot.job
  const done = job.progress.succeeded + job.progress.failed
  const percent = progressPercent(done, job.progress.total)
  const status = job.progress.failed > 0 ? 'error' : job.status === 'succeeded' ? 'success' : 'default'
  const suffix = snapshot.stale ? ' · 疑似掉线' : ''
  return h(NSpace, { vertical: true, size: 2 }, {
    default: () => [
      h(NProgress, { type: 'line', percentage: percent, height: 8, showIndicator: false, status }),
      h(NText, { depth: 3, style: 'font-size: 12px' }, {
        default: () => `${formatProgress(done, job.progress.total)}${suffix}`,
      }),
    ],
  })
}

const columns: DataTableColumns<Run> = [
  { title: 'ID', key: 'id', width: 60 },
  { title: '数据集', key: 'dataset_id', width: 80 },
  {
    title: '状态',
    key: 'status',
    width: 90,
    render: (row) =>
        h(NTag, { size: 'small', type: statusTagType(row.status) }, { default: () => statusLabel(row.status) }),
  },
  { title: '进度', key: 'progress', width: 170, render: (row) => renderProgress(row) },
  {
    title: 'Recall@k',
    key: 'recall',
    width: 100,
    render: (row) => formatPercent(row.metrics?.recall_at_k),
  },
  { title: 'MRR@k', key: 'mrr', width: 90, render: (row) => formatPercent(row.metrics?.mrr_at_k) },
  { title: 'Hit@k', key: 'hit', width: 90, render: (row) => formatPercent(row.metrics?.hit_at_k) },
  {
    title: '代码版本',
    key: 'git_sha',
    width: 100,
    render: (row) =>
        h(NTooltip, {}, {
          trigger: () => h(NText, { code: true }, { default: () => shortHash(row.git_sha) }),
          default: () => row.git_sha || '—',
        }),
  },
  {
    title: '配置指纹',
    key: 'config_hash',
    width: 110,
    render: (row) =>
        h(NTooltip, {}, {
          trigger: () => h(NText, { code: true }, { default: () => shortHash(row.config_hash) }),
          default: () => row.config_hash || '—',
        }),
  },
  { title: '创建时间', key: 'created_at', width: 150, render: (row) => formatDateTime(row.created_at) },
  {
    title: '操作',
    key: 'actions',
    width: 100,
    render: (row) =>
        h(
            NButton,
            { size: 'small', type: 'primary', quaternary: true, onClick: () => router.push(`/runs/${row.id}`) },
            { default: () => '查看报告' },
        ),
  },
]
</script>

<template>
  <NCard title="评测运行">
    <template #header-extra>
      <NSpace align="center">
        <NTag v-if="hasActiveRun && autoRefresh" size="small" type="info">自动刷新中</NTag>
        <NSpace align="center" :size="4">
          <NText depth="3" style="font-size: 12px">自动刷新</NText>
          <NSwitch v-model:value="autoRefresh" size="small" />
        </NSpace>
        <NInputNumber
            v-model:value="datasetFilter"
            :min="1"
            placeholder="按数据集 ID 过滤"
            style="width: 170px"
            clearable
        />
        <NButton @click="load()">查询</NButton>
        <NButton type="primary" :loading="loading" @click="refresh">刷新</NButton>
      </NSpace>
    </template>

    <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
      {{ errorText }}
    </NAlert>

    <NText depth="3" style="display: block; margin-bottom: 8px">
      每次评测都会留下一条 run：包含全量配置快照、配置指纹与代码版本；有任务在跑时进度条会自动刷新。
    </NText>

    <NDataTable
        :columns="columns"
        :data="runs"
        :loading="loading"
        :row-key="(row: Run) => row.id"
        size="small"
    />
  </NCard>
</template>