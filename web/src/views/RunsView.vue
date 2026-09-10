<script setup lang="ts">
import { h, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NInputNumber,
  NSpace,
  NTag,
  NText,
  NTooltip,
} from 'naive-ui'
import { listRuns } from '../api/runs'
import type { Run } from '../api/types'
import { formatDateTime, formatPercent, shortHash, statusLabel, statusTagType } from '../utils/format'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const runs = ref<Run[]>([])
const loading = ref(false)
const errorText = ref('')
const datasetFilter = ref<number | null>(
    route.query.dataset_id ? Number(route.query.dataset_id) : null,
)

async function load() {
  loading.value = true
  errorText.value = ''
  try {
    runs.value = await listRuns({ datasetId: datasetFilter.value ?? undefined, limit: 50 })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function refresh() {
  await load()
  message.success(`已加载 ${runs.value.length} 条运行记录`)
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
        <NInputNumber
            v-model:value="datasetFilter"
            :min="1"
            placeholder="按数据集 ID 过滤"
            style="width: 170px"
            clearable
        />
        <NButton @click="load">查询</NButton>
        <NButton type="primary" :loading="loading" @click="refresh">刷新</NButton>
      </NSpace>
    </template>

    <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
      {{ errorText }}
    </NAlert>

    <NText depth="3" style="display: block; margin-bottom: 8px">
      每次评测都会留下一条 run：包含全量配置快照、配置指纹与代码版本，用于复现与 A/B 对比。
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