<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NInput,
  NList,
  NListItem,
  NSpace,
  NTag,
  NText,
} from 'naive-ui'
import { deleteCase, getDataset, importCases, listCases } from '../api/datasets'
import type { CaseItem, Dataset, ImportReport } from '../api/types'

const route = useRoute()
const message = useMessage()

const datasetId = computed(() => Number(route.params.id))
const dataset = ref<Dataset | null>(null)
const cases = ref<CaseItem[]>([])
const loading = ref(false)
const errorText = ref('')

async function loadAll() {
  loading.value = true
  errorText.value = ''
  try {
    dataset.value = await getDataset(datasetId.value)
    cases.value = await listCases(datasetId.value)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}
onMounted(loadAll)

// ---- 导入 ----
const jsonlText = ref('')
const importing = ref(false)
const report = ref<ImportReport | null>(null)

async function doImport() {
  if (!jsonlText.value.trim()) {
    message.warning('请先粘贴 JSONL 内容')
    return
  }
  importing.value = true
  report.value = null
  try {
    report.value = await importCases(datasetId.value, jsonlText.value)
    message.success(`导入完成：成功 ${report.value.imported}/${report.value.total}`)
    await loadAll()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    importing.value = false
  }
}

function onFilePicked(file: File) {
  const reader = new FileReader()
  reader.onload = () => {
    jsonlText.value = String(reader.result ?? '')
  }
  reader.readAsText(file)
}

async function onDeleteCase(row: CaseItem) {
  if (!window.confirm(`确认删除用例 ${row.qid}？`)) return
  try {
    await deleteCase(row.id)
    message.success('已删除')
    await loadAll()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

const difficultyTagType = (d: string | undefined) =>
    d === '易' ? 'success' : d === '中' ? 'warning' : d === '难' ? 'error' : 'default'

const columns: DataTableColumns<CaseItem> = [
  { title: 'ID', key: 'id', width: 70 },
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
    title: 'gold 锚点',
    key: 'gold_anchors',
    width: 160,
    ellipsis: { tooltip: true },
    render: (row) => (row.gold_anchors ?? []).map((a) => a.doc).join(', '),
  },
  {
    title: '操作',
    key: 'actions',
    width: 90,
    render: (row) =>
        h(
            NButton,
            { size: 'small', type: 'error', quaternary: true, onClick: () => onDeleteCase(row) },
            { default: () => '删除' },
        ),
  },
]
</script>

<template>
  <div>
    <NCard v-if="dataset" :title="`数据集 · ${dataset.name}`" style="margin-bottom: 16px">
      <NText depth="3">
        {{ dataset.description || '（无描述）' }} · 用例数 {{ dataset.case_count ?? cases.length }}
      </NText>
      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-top: 12px">
        {{ errorText }}
      </NAlert>
    </NCard>

    <NCard title="导入评测用例（JSONL）" style="margin-bottom: 16px">
      <NSpace vertical>
        <NText depth="3">每行一个 JSON 对象：qid / question / gold_anchors[].doc / difficulty / reference_answer</NText>
        <NInput
            v-model:value="jsonlText"
            type="textarea"
            :rows="6"
            placeholder='{"qid":"c-001","question":"...","gold_anchors":[{"doc":"A01"}],"difficulty":"易"}'
        />
        <NSpace>
          <label>
            <NButton size="small">选择 .jsonl 文件</NButton>
            <input
                type="file"
                accept=".jsonl,.txt,application/json"
                style="display: none"
                @change="(e: Event) => onFilePicked((e.target as HTMLInputElement).files![0])"
            />
          </label>
          <NButton type="primary" :loading="importing" @click="doImport">导入</NButton>
        </NSpace>
      </NSpace>

      <template v-if="report">
        <NAlert
            :type="report.imported === report.total ? 'success' : 'warning'"
            style="margin-top: 12px"
        >
          <NText strong>导入报告：成功 {{ report.imported }} / 总数 {{ report.total }}</NText>
        </NAlert>
        <NList v-if="report.errors.length" bordered size="small" style="margin-top: 8px">
          <NListItem v-for="(e, i) in report.errors" :key="i">
            <NText depth="3">
              第 {{ e.line }} 行（{{ e.qid || '—' }}）：<NText type="error">{{ e.reason }}</NText>
            </NText>
          </NListItem>
        </NList>
      </template>
    </NCard>

    <NCard title="用例列表">
      <NDataTable
          :columns="columns"
          :data="cases"
          :loading="loading"
          :row-key="(row: CaseItem) => row.id"
          size="small"
      />
    </NCard>
  </div>
</template>