<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
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
  NSelect,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { listRuns } from '../api/runs'
import { getClosure } from '../api/closure'
import { listAnnotations, updateAnnotation } from '../api/annotations'
import type { Annotation, ClosureRecord, ClosureReport, Run } from '../api/types'
import { shortHash } from '../utils/format'
import { metricLabel } from '../utils/compare'
import { statusLabel, statusTagType } from '../utils/annotation'
import { flagsText, evidenceText, evidenceHighlights, closureActionText, closureCards,
  closureGuard, closureSummaryLine, closureVerdictLabel, closureVerdictType, verifyHint,
  candidateRunOptions } from '../utils/closure'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const runs = ref<Run[]>([])
const baselineId = ref<number | null>(route.query.baseline ? Number(route.query.baseline) : null)
const candidateId = ref<number | null>(route.query.candidate ? Number(route.query.candidate) : null)
const report = ref<ClosureReport | null>(null)
const annotations = ref<Annotation[]>([])
const loading = ref(false)
const verifying = ref(false)
const errorText = ref('')
const fixedOnly = ref(false)

const runOptions = computed(() =>
    runs.value.map((run) => ({
      label: `#${run.id} · dataset ${run.dataset_id} · ${shortHash(run.config_hash)} · ${run.status}`,
      value: run.id,
    })),
)

const baselineRun = computed(() => runs.value.find((run) => run.id === baselineId.value) ?? null)

/**
 * 候选 run 的选项: 同集优先, 并**明确标出同配置的那几次**。
 * 拿两次配置相同的 run 做闭环, 得到的"变化"只可能是噪声, 而它长得跟真效果一样。
 */
const candidateOptions = computed(() =>
    candidateRunOptions(runs.value, baselineRun.value, baselineId.value ?? -1),
)

async function loadRuns() {
  try {
    runs.value = await listRuns({ limit: 200 })
    if (!baselineId.value) {
      const judged = runs.value.find((run) => Number(run.metrics?.cases_judged ?? 0) > 0)
      baselineId.value = judged?.id ?? runs.value[0]?.id ?? null
    }
    if (!candidateId.value) {
      // 默认挑同集里"配置不同"的那次 —— 那才是一次真实验, 而不是复现
      const baseline = runs.value.find((run) => run.id === baselineId.value) ?? null
      const candidates = candidateRunOptions(runs.value, baseline, baselineId.value ?? -1)
      const experiment = candidates.find((option) => {
        const run = runs.value.find((item) => item.id === option.value)
        return run !== undefined && (!baseline || run.config_hash !== baseline.config_hash)
      })
      candidateId.value = experiment?.value ?? candidates[0]?.value ?? null
    }
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  }
}

async function loadClosure() {
  if (!baselineId.value || !candidateId.value) return
  loading.value = true
  errorText.value = ''
  try {
    const [closureReport, annotationList] = await Promise.all([
      getClosure({
        baseline: baselineId.value,
        candidate: candidateId.value,
        status: fixedOnly.value ? 'fixed' : undefined,
      }),
      listAnnotations({ runId: baselineId.value, limit: 500 }),
    ])
    report.value = closureReport
    annotations.value = annotationList
    router.replace({
      query: { baseline: String(baselineId.value), candidate: String(candidateId.value) },
    })
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

/** 一键销单: 把"确认变好"的题推进到 verified(fixed → verified 是状态机允许的唯一主链路)。 */
async function verifyAll() {
  const records = (report.value?.records ?? []).filter((record) => record.eligible_for_verify)
  if (records.length === 0) return
  verifying.value = true
  let ok = 0
  const failures: string[] = []
  for (const record of records) {
    try {
      await updateAnnotation(record.annotation_id, {
        status: 'verified',
        comment: record.comment || `闭环核对: 在 run #${candidateId.value} 上已确认变好`,
      })
      ok++
    } catch (err) {
      failures.push(`${record.qid}: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  verifying.value = false
  if (failures.length === 0) {
    message.success(`已把 ${ok} 题推进到 verified`)
  } else {
    message.error(`${ok} 题成功, ${failures.length} 题失败 —— ${failures.join('；')}`)
  }
  await loadClosure()
}

const guard = computed(() => (report.value ? closureGuard(report.value) : null))
const cards = computed(() => (report.value ? closureCards(report.value.summary) : []))
const summaryLine = computed(() => (report.value ? closureSummaryLine(report.value.summary) : ''))
const actionText = computed(() => (report.value ? closureActionText(report.value.summary) : ''))
const eligibleCount = computed(() => report.value?.summary.eligible_for_verify ?? 0)

async function markVerified(record: ClosureRecord) {
  try {
    await updateAnnotation(record.annotation_id, {
      status: 'verified',
      comment: record.comment || `闭环核对: 在 run #${candidateId.value} 上已确认变好`,
    })
    message.success(`${record.qid} 已推进到 verified`)
    await loadClosure()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

onMounted(async () => {
  await loadRuns()
  await loadClosure()
})

const columns: DataTableColumns<ClosureRecord> = [
  { title: 'qid', key: 'qid', width: 100, render: (row) => row.qid },
  {
    title: '标注状态',
    key: 'status',
    width: 100,
    render: (row) =>
        h(NTag, { size: 'small', type: statusTagType(row.status) }, { default: () => statusLabel(row.status) }),
  },
  { title: '标签变化', key: 'flags', width: 240, render: (row) => `${flagsText(row.flags_left)} → ${flagsText(row.flags_right)}` },
  {
    title: '结局',
    key: 'verdict',
    width: 110,
    render: (row) =>
        h(NTag, { size: 'small', type: closureVerdictType(row.verdict) },
            { default: () => closureVerdictLabel(row.verdict) }),
  },
  {
    title: '关键证据',
    key: 'evidence',
    ellipsis: { tooltip: true },
    render: (row) => {
      const highlights = evidenceHighlights(row.evidence, 3)
      if (highlights.length === 0) return '—'
      return highlights.map((item) => evidenceText(item, metricLabel)).join(' ｜ ')
    },
  },
  {
    title: '能不能销单',
    key: 'action',
    width: 260,
    render: (row) => {
      const hint = verifyHint(row)
      return h(NSpace, { size: 4, align: 'center' }, {
        default: () => [
          h(NTag, { size: 'small', type: hint.type }, { default: () => hint.text }),
          row.eligible_for_verify
              ? h(NButton, { size: 'tiny', type: 'primary', quaternary: true, onClick: () => markVerified(row) },
                  { default: () => '标记已修好' })
              : null,
        ],
      })
    },
  },
]
</script>

<template>
  <div>
    <NCard title="标注闭环：修好了吗？" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NText depth="3" style="font-size: 12px">只看 fixed</NText>
          <NSwitch v-model:value="fixedOnly" size="small" @update:value="loadClosure" />
          <NButton size="small" :loading="loading" @click="loadClosure">刷新</NButton>
        </NSpace>
      </template>

      <NSpace align="center" :size="12" style="margin-bottom: 12px">
        <NText depth="3" style="font-size: 12px">标注在哪次 run</NText>
        <NSelect
            v-model:value="baselineId"
            :options="runOptions"
            filterable
            placeholder="baseline(打标注的那次)"
            style="width: 300px"
            @update:value="loadClosure"
        />
        <NText depth="3" style="font-size: 12px">之后哪次 run</NText>
        <NSelect
            v-model:value="candidateId"
            :options="candidateOptions"
            filterable
            placeholder="candidate(改完之后的那次)"
            style="width: 300px"
            @update:value="loadClosure"
        />
      </NSpace>

      <NText depth="3" style="display: block; margin-bottom: 12px; font-size: 12px">
        方向不能反：baseline 是"当时发现坏题的那次 run"（标注挂在它上面），candidate 是"改完之后的新 run"。
        反了会把修好的题读成弄坏的题。
      </NText>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">{{ errorText }}</NAlert>
      <NAlert v-if="guard" :type="guard.type" :show-icon="false" style="margin-bottom: 12px">
        <NText style="font-size: 13px">{{ guard.text }}</NText>
      </NAlert>

      <NGrid v-if="report" :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <NGi v-for="card in cards" :key="card.label" span="4 s:2 m:1">
          <NStatistic :label="card.label" :value="String(card.value)" />
          <NText depth="3" style="font-size: 12px">{{ card.hint }}</NText>
        </NGi>
      </NGrid>

      <NSpace align="center" justify="space-between" style="margin-top: 14px">
        <NText style="font-size: 13px">{{ summaryLine }}</NText>
        <NSpace align="center">
          <NButton
              v-if="eligibleCount > 0"
              size="small"
              type="primary"
              :loading="verifying"
              @click="verifyAll"
          >
            一键验证这 {{ eligibleCount }} 题
          </NButton>
          <NTag v-if="report" size="small">
            {{ shortHash(report.baseline_hash) }} → {{ shortHash(report.candidate_hash) }}
          </NTag>
        </NSpace>
      </NSpace>
      <NText depth="3" style="display: block; margin-top: 8px; font-size: 12px">{{ actionText }}</NText>
    </NCard>

    <NCard title="逐题核对" style="margin-bottom: 16px">
      <NEmpty v-if="!report || report.records.length === 0" description="这次 run 上没有可核对的标注" />
      <NDataTable
          v-else
          :columns="columns"
          :data="report.records"
          :loading="loading"
          :row-key="(row: ClosureRecord) => String(row.annotation_id)"
          :scroll-x="1100"
          size="small"
      />
    </NCard>

    <NCard v-if="report && report.notes.length" title="提醒" size="small">
      <NSpace vertical :size="8">
        <NAlert v-for="note in report.notes" :key="note" type="warning" :show-icon="false">
          <NText style="font-size: 12px">{{ note }}</NText>
        </NAlert>
      </NSpace>
    </NCard>

    <NCard v-if="annotations.length" size="small" style="margin-top: 16px">
      <NText depth="3" style="font-size: 12px">
        baseline run #{{ baselineId }} 上共有 {{ annotations.length }} 条标注（含未修 / 已作废的），
        它们不会出现在上面的清单里 —— 闭环只核对"这次改动动到的题"。
      </NText>
    </NCard>
  </div>
</template>
