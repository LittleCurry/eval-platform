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
  NSelect,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import {
  getAnnotationStats,
  getAnnotationSuggestion,
  listAnnotations,
  upsertAnnotation,
} from '../api/annotations'
import { getRunCaseResults, listRuns } from '../api/runs'
import type { Annotation, AnnotationStats, AnnotationSuggestion, Run, RunCaseResult } from '../api/types'
import { flagLabel, primaryFlag } from '../utils/report'
import { formatPercent, shortHash } from '../utils/format'
import {
  REASONS,
  SHORTCUT_HELP,
  joinAnnotations,
  nextStatuses,
  pendingFirst,
  progressText,
  reasonLabel,
  reasonLine,
  reasonTagType,
  shortcutFor,
  statsLine,
  statusLabel,
  statusTagType,
  type AnnotationRow,
} from '../utils/annotation'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const runs = ref<Run[]>([])
const runId = ref<number | null>(route.query.run ? Number(route.query.run) : null)
const cases = ref<RunCaseResult[]>([])
const annotations = ref<Annotation[]>([])
const stats = ref<AnnotationStats | null>(null)
const loading = ref(false)
const errorText = ref('')
const flaggedOnly = ref(true)
const onlyPending = ref(false)

const selectedCaseId = ref<number | null>(null)
const suggestion = ref<AnnotationSuggestion | null>(null)
const commentDraft = ref('')
const saving = ref(false)

const runOptions = computed(() =>
    runs.value.map((run) => ({
      label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)} · ${run.status}`,
      value: run.id,
    })),
)

const rows = computed<AnnotationRow[]>(() => {
  const joined = joinAnnotations(cases.value, annotations.value)
  const filtered = onlyPending.value
      ? joined.filter((row) => row.annotation === null || row.annotation.status === 'open')
      : joined
  return pendingFirst(filtered)
})
const filteredRows = computed(() => rows.value)

const selected = computed(() => {
  if (selectedCaseId.value === null) return null
  return rows.value.find((row) => row.case.case_id === selectedCaseId.value) ?? null
})

const casesWithFlags = computed(() => cases.value.filter((item) => (item.flags ?? []).length > 0).length)

async function loadRuns() {
  try {
    runs.value = await listRuns({ limit: 200 })
    if (!runId.value) {
      // 默认挑"最近的、跑过生成或判定的"那次: 只有那种 run 才有归因标签可标
      const judged = runs.value.find((run) => run.metrics?.answers_generated || run.metrics?.cases_judged)
      runId.value = judged?.id ?? runs.value[0]?.id ?? null
    }
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  }
}

async function loadWorkbench() {
  if (!runId.value) return
  loading.value = true
  errorText.value = ''
  try {
    const [caseList, annotationList, statsPayload] = await Promise.all([
      getRunCaseResults(runId.value, { limit: 500, flaggedOnly: flaggedOnly.value }),
      listAnnotations({ runId: runId.value, limit: 500 }),
      getAnnotationStats(runId.value),
    ])
    cases.value = caseList
    annotations.value = annotationList
    stats.value = statsPayload
    // 选中项失效(换 run / 过滤后不在了)时自动落到第一行
    const stillVisible = rows.value.some((row) => row.case.case_id === selectedCaseId.value)
    if (!stillVisible) selectRow(rows.value[0] ?? null)
    router.replace({ query: { run: String(runId.value) } })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function selectRow(row: AnnotationRow | null) {
  selectedCaseId.value = row?.case.case_id ?? null
  commentDraft.value = row?.annotation?.comment ?? ''
  suggestion.value = null
  if (!row || !runId.value) return
  try {
    suggestion.value = await getAnnotationSuggestion(runId.value, row.case.case_id)
  } catch {
    // 建议拿不到不影响打标: 人工自己选归因即可
    suggestion.value = null
  }
}

/** 打标/改状态都走 upsert(服务端按 (run, case) 判定新建还是更新)。 */
async function upsert(payload: { status?: string; reason?: string; comment?: string }) {
  if (!runId.value || !selected.value) return
  saving.value = true
  try {
    await upsertAnnotation({ run_id: runId.value, case_id: selected.value.case.case_id, ...payload })
    annotations.value = await listAnnotations({ runId: runId.value, limit: 500 })
    stats.value = await getAnnotationStats(runId.value)
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    saving.value = false
  }
}

async function markReason(reason: string) {
  // 第一次打标自动开单(open), 之后的 reason 修改沿用当前状态
  const current = selected.value?.annotation?.status
  await upsert(current ? { reason } : { status: 'open', reason })
  next()
}

async function markStatus(status: string) {
  await upsert({ status })
  if (status === 'verified') next() // 验证完就走, 这是工作台的主循环
}

async function saveComment() {
  await upsert({ comment: commentDraft.value })
  message.success('评论已保存')
}

function next() {
  const list = rows.value
  if (list.length === 0) return
  const index = list.findIndex((row) => row.case.case_id === selectedCaseId.value)
  const target = list[(index + 1) % list.length]
  if (target) selectRow(target)
}

const availableStatuses = computed(() => nextStatuses(selected.value?.annotation?.status))

function onKeydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  const tag = target?.tagName ?? ''
  // 在输入框里打字时不要抢键(否则评论里的数字会变成"改用检索问题")
  if (tag === 'INPUT' || tag === 'TEXTAREA' || target?.isContentEditable) return
  const hit = shortcutFor(event.key)
  if (!hit) return
  event.preventDefault()
  if (hit.kind === 'next') return next()
  if (hit.kind === 'reason') return void markReason(hit.value)
  void markStatus(hit.value)
}

onMounted(async () => {
  await loadRuns()
  await loadWorkbench()
  window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

const columns: DataTableColumns<AnnotationRow> = [
  { title: 'qid', key: 'qid', width: 100, render: (row) => row.case.qid },
  {
    title: '主因(机器)',
    key: 'flag',
    width: 150,
    render: (row) => {
      const flag = primaryFlag(row.case.flags)
      return flag ? h(NTag, { size: 'small', type: 'warning' }, { default: () => flagLabel(flag) }) : '—'
    },
  },
  {
    title: '标注状态',
    key: 'status',
    width: 100,
    render: (row) =>
        h(NTag, { size: 'small', type: statusTagType(row.annotation?.status) },
            { default: () => statusLabel(row.annotation?.status) }),
  },
  {
    title: '人工归因',
    key: 'reason',
    width: 110,
    render: (row) =>
        row.annotation?.reason
            ? h(NTag, { size: 'small', type: reasonTagType(row.annotation.reason) },
                { default: () => reasonLabel(row.annotation?.reason) })
            : h(NText, { depth: 3 }, { default: () => '—' }),
  },
  { title: '评论', key: 'comment', ellipsis: { tooltip: true }, render: (row) => row.annotation?.comment || '—' },
  { title: '问题', key: 'question', width: 260, ellipsis: { tooltip: true }, render: (row) => row.case.question },
]

function onRowProps(row: AnnotationRow) {
  return {
    style: 'cursor: pointer',
    onClick: () => selectRow(row),
  }
}
</script>

<template>
  <div>
    <NCard title="Bad Case 标注工作台" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NText depth="3" style="font-size: 12px">只看有标签的</NText>
          <NSwitch v-model:value="flaggedOnly" size="small" @update:value="loadWorkbench" />
          <NButton size="small" :loading="loading" @click="loadWorkbench">刷新</NButton>
        </NSpace>
      </template>

      <NSpace align="center" :size="12" style="margin-bottom: 12px">
        <NSelect
            v-model:value="runId"
            :options="runOptions"
            filterable
            placeholder="选择一次 run"
            style="width: 340px"
            @update:value="loadWorkbench"
        />
        <NTag v-if="runId" size="small" type="info">{{ progressText(rows) }}</NTag>
        <NTag v-if="casesWithFlags" size="small" type="warning">有标签的题 {{ casesWithFlags }}</NTag>
      </NSpace>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">{{ errorText }}</NAlert>

      <NGrid :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi span="4 s:2 m:1">
          <NStatistic label="标注总数" :value="String(stats?.total ?? 0)" />
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic label="待处理" :value="String(stats?.by_status?.open ?? 0)" />
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic label="已修" :value="String(stats?.by_status?.fixed ?? 0)" />
        </NGi>
        <NGi span="4 s:2 m:1">
          <NStatistic label="已验证" :value="String(stats?.by_status?.verified ?? 0)" />
        </NGi>
      </NGrid>
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        {{ statsLine(stats) }}<template v-if="reasonLine(stats)"> ｜ 归因：{{ reasonLine(stats) }}</template>
      </NText>
    </NCard>

    <NGrid :cols="5" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
      <NGi span="5 m:3">
        <NCard title="待标注的题" style="margin-bottom: 16px">
          <template #header-extra>
            <NSpace align="center">
              <NText depth="3" style="font-size: 12px">只看待处理</NText>
              <NSwitch v-model:value="onlyPending" size="small" />
            </NSpace>
          </template>
          <NDataTable
              :columns="columns"
              :data="filteredRows"
              :loading="loading"
              :row-key="(row: AnnotationRow) => row.case.case_id"
              :row-props="onRowProps"
              :scroll-x="820"
              :row-class-name="(row: AnnotationRow) => (row.case.case_id === selectedCaseId ? 'row-active' : '')"
              size="small"
          />
        </NCard>
      </NGi>

      <NGi span="5 m:2">
        <NCard :title="selected ? `打标 · ${selected.case.qid}` : '打标'" style="margin-bottom: 16px">
          <NEmpty v-if="!selected" description="从左边选一道题" />
          <template v-else>
            <NText style="display: block; margin-bottom: 8px">{{ selected.case.question }}</NText>

            <NSpace size="small" style="margin-bottom: 10px">
              <NTag v-if="primaryFlag(selected.case.flags)" size="small" type="warning">
                主因：{{ flagLabel(primaryFlag(selected.case.flags) as string) }}
              </NTag>
              <NTag size="small">Recall {{ formatPercent(selected.case.metrics?.recall) }}</NTag>
              <NTag size="small" :type="statusTagType(selected.annotation?.status)">
                {{ statusLabel(selected.annotation?.status) }}
              </NTag>
            </NSpace>

            <NAlert v-if="suggestion" type="info" :show-icon="false" style="margin-bottom: 10px">
              <NText style="font-size: 12px">
                机器建议：<b>{{ reasonLabel(suggestion.reason) }}</b> —— {{ suggestion.rationale }}
              </NText>
              <NButton
                  size="tiny"
                  quaternary
                  style="margin-left: 8px"
                  @click="markReason(suggestion.reason || 'unknown')"
              >
                采纳
              </NButton>
            </NAlert>

            <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">人工归因（1-5）</NText>
            <NSpace :size="6" style="margin-bottom: 12px">
              <NButton
                  v-for="(reason, index) in REASONS"
                  :key="reason"
                  size="small"
                  :type="selected.annotation?.reason === reason ? 'primary' : 'default'"
                  :loading="saving"
                  @click="markReason(reason)"
              >
                {{ index + 1 }} {{ reasonLabel(reason) }}
              </NButton>
            </NSpace>

            <NText depth="3" style="display: block; margin-bottom: 6px; font-size: 12px">
              状态流转（当前可流转：{{ availableStatuses.map(statusLabel).join(' / ') || '—' }}）
            </NText>
            <NSpace :size="6" style="margin-bottom: 12px">
              <NButton
                  v-for="status in availableStatuses"
                  :key="status"
                  size="small"
                  :type="statusTagType(status)"
                  :loading="saving"
                  @click="markStatus(status)"
              >
                {{ statusLabel(status) }}
              </NButton>
            </NSpace>

            <NInput
                v-model:value="commentDraft"
                type="textarea"
                :autosize="{ minRows: 2, maxRows: 5 }"
                placeholder="评论（这是要留给人看的话：为什么这么判、下一步怎么改）"
                style="margin-bottom: 8px"
            />
            <NSpace justify="end">
              <NButton size="small" :loading="saving" @click="saveComment">保存评论</NButton>
              <NButton size="small" type="primary" quaternary @click="next">下一题 (n)</NButton>
            </NSpace>
          </template>
        </NCard>
      </NGi>
    </NGrid>

    <NCard size="small">
      <NText depth="3" style="font-size: 12px">{{ SHORTCUT_HELP }}</NText>
    </NCard>
  </div>
</template>

<style scoped>
:deep(.row-active td) {
  background: rgba(127, 127, 127, 0.12);
}
</style>