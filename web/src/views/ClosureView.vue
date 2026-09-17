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
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NStatistic,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { getRun, listRuns, submitRun } from '../api/runs'
import { getClosure } from '../api/closure'
import { listAnnotations, updateAnnotation } from '../api/annotations'
import { listPipelineProfiles, previewPipeline } from '../api/pipelineProfiles'
import type { Annotation, ClosureRecord, ClosureReport, PipelineProfile, Run } from '../api/types'
import { shortHash } from '../utils/format'
import { metricLabel } from '../utils/compare'
import { usePermission } from '../composables/usePermission'
import { configToForm, type ProfileForm } from '../utils/profile'
import { describeChanges, formFromRunSnapshot, formToRunPayload, rerunGuard, sourceFromRunSnapshot } from '../utils/rerun'
import { statusLabel, statusTagType } from '../utils/annotation'
import { flagsText, evidenceText, evidenceHighlights, closureActionText, closureCards,
  closureGuard, closureSummaryLine, closureVerdictLabel, closureVerdictType, verifyHint,
  candidateRunOptions } from '../utils/closure'

const route = useRoute()
const router = useRouter()
const message = useMessage()
// M7-3: 销单与重跑都是写操作
const { canWrite, canSubmit } = usePermission()

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

// ---- 改配置重跑(M6 闭环的中间一环: 发现没修好 -> 调参数 -> 重跑 -> 再核对) ----

const showRerun = ref(false)
const baselineSnapshot = ref<Record<string, unknown> | null>(null)
const rerunForm = ref<ProfileForm | null>(null)
const baselineForm = ref<ProfileForm | null>(null)
const profiles = ref<PipelineProfile[]>([])
const previewHash = ref<string>('')
const previewing = ref(false)
const submitting = ref(false)
const submittedRun = ref<number | null>(null)

const baselineSource = computed(() => sourceFromRunSnapshot(baselineSnapshot.value ?? undefined))
const changes = computed(() =>
    rerunForm.value && baselineForm.value ? describeChanges(rerunForm.value, baselineForm.value) : [],
)
const rerunWarning = computed(() =>
    report.value && rerunForm.value
        ? rerunGuard(report.value.baseline_hash, previewHash.value || undefined, changes.value.length)
        : null,
)

/** 打开对话框时从上一版快照预填 —— 重跑必须"只改一个变量, 其余原样带过去"。 */
async function openRerun() {
  if (!baselineId.value) return
  errorText.value = ''
  showRerun.value = true
  submittedRun.value = null
  previewHash.value = ''
  try {
    const run = await getRun(baselineId.value)
    baselineSnapshot.value = (run.config_snapshot ?? null) as Record<string, unknown> | null
    const form = formFromRunSnapshot(baselineSnapshot.value ?? undefined)
    baselineForm.value = form
    rerunForm.value = { ...form, chunking: { ...form.chunking }, generation: { ...form.generation }, judge: { ...form.judge } }
    profiles.value = await listPipelineProfiles(run.project_id || 1)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  }
}

/** 套用配置模板: 整段替换(模板本来就是"一整套配置")。 */
function applyProfile(profileId: number | null) {
  const profile = profiles.value.find((item) => item.id === profileId)
  if (!profile || !rerunForm.value) return
  rerunForm.value = configToForm(profile.config)
  previewHash.value = ''
}

/** 提交前算指纹: 与上一版相同就说明这是一次复现, 不会有任何可验证的变化。 */
async function runPreview() {
  if (!rerunForm.value) return
  const payload = formToRunPayload(rerunForm.value, baselineSource.value)
  if (!payload) {
    message.error('快照里缺数据来源(dataset/corpus), 无法安全重跑')
    return
  }
  previewing.value = true
  try {
    const preview = await previewPipeline(payload)
    previewHash.value = preview.config_hash
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    previewing.value = false
  }
}

async function doSubmit() {
  if (!rerunForm.value) return
  const payload = formToRunPayload(rerunForm.value, baselineSource.value)
  if (!payload) {
    message.error('快照里缺数据来源(dataset/corpus), 无法安全重跑')
    return
  }
  submitting.value = true
  try {
    const ref = await submitRun(payload)
    submittedRun.value = ref.run_id
    message.success(`已提交 run #${ref.run_id}(${ref.items} 题), 等 worker 跑完后把它设为对照即可核对`)
    await loadRuns()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    submitting.value = false
  }
}

/** 把刚提交的 run 设为 candidate: 闭环页立刻转到"新版本 vs 标注"的视角。 */
function useSubmittedAsCandidate() {
  if (!submittedRun.value) return
  candidateId.value = submittedRun.value
  showRerun.value = false
  void loadClosure()
}

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
              ? h(NButton, {
                    size: 'tiny', type: 'primary', quaternary: true,
                    disabled: !canWrite.value,
                    onClick: () => markVerified(row),
                  }, { default: () => '标记已修好' })
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
          <NButton size="small" :disabled="!canSubmit" @click="openRerun">改配置重跑</NButton>
          <NButton
              v-if="eligibleCount > 0"
              size="small"
              type="primary"
              :loading="verifying"
              :disabled="!canWrite"
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

    <NModal
        v-model:show="showRerun"
        preset="card"
        title="改配置重跑（只改一个变量，其余沿用上一版快照）"
        style="width: 640px"
    >
      <NEmpty v-if="!rerunForm" description="正在读取上一版的配置快照…" />
      <template v-else>
        <NAlert v-if="!baselineSource" type="error" :show-icon="false" style="margin-bottom: 10px">
          上一版快照里没有 dataset/corpus, 不能重跑(宁可不跑, 也不能跑错数据集)
        </NAlert>
        <NText depth="3" style="display: block; margin-bottom: 10px; font-size: 12px">
          数据来源：dataset {{ baselineSource?.datasetId ?? '—' }} · corpus {{ baselineSource?.corpusId ?? '—' }}
          （与上一版一致, 重跑只改参数）
        </NText>

        <NSpace align="center" style="margin-bottom: 10px">
          <NText depth="3" style="font-size: 12px; width: 70px">套用模板</NText>
          <NSelect
              :options="profiles.map((item) => ({ label: `${item.name}（${item.description || '无描述'}）`, value: item.id }))"
              placeholder="可选：用配置模板整段替换"
              style="width: 380px"
              clearable
              @update:value="applyProfile"
          />
        </NSpace>

        <NSpace align="center" style="margin-bottom: 10px">
          <NText depth="3" style="font-size: 12px; width: 70px">top_k</NText>
          <NInputNumber v-model:value="rerunForm.topK" :min="1" :max="50" size="small" style="width: 120px" />
          <NText depth="3" style="font-size: 12px">上一版 {{ baselineForm?.topK }}</NText>
        </NSpace>

        <NSpace align="center" style="margin-bottom: 10px">
          <NText depth="3" style="font-size: 12px; width: 70px">生成</NText>
          <NSwitch v-model:value="rerunForm.generationEnabled" size="small" />
          <NText depth="3" style="font-size: 12px">
            {{ rerunForm.generationEnabled ? rerunForm.generation.model : '不跑生成(只测检索)' }}
          </NText>
        </NSpace>

        <NSpace align="center" style="margin-bottom: 12px">
          <NText depth="3" style="font-size: 12px; width: 70px">判定</NText>
          <NSwitch v-model:value="rerunForm.judgeEnabled" size="small" />
          <NText depth="3" style="font-size: 12px">
            {{ rerunForm.judgeEnabled ? `${rerunForm.judge.model} · ${rerunForm.judge.claims_prompt_id}` : '不跑判定' }}
          </NText>
        </NSpace>

        <NAlert v-if="changes.length === 0" type="warning" :show-icon="false" style="margin-bottom: 10px">
          一个参数都没改：这会是上一版的复现（指纹相同），闭环页只会看到噪声。
        </NAlert>
        <NAlert v-else type="info" :show-icon="false" style="margin-bottom: 10px">
          改动：{{ changes.join('；') }}
        </NAlert>

        <NSpace align="center" style="margin-bottom: 10px">
          <NButton size="small" :loading="previewing" @click="runPreview">算指纹（提交前确认这是新实验）</NButton>
          <NText v-if="previewHash" depth="3" style="font-size: 12px">
            将落库的 config_hash = {{ previewHash.slice(0, 12) }}… ｜ 上一版 {{ report?.baseline_hash.slice(0, 12) }}…
          </NText>
        </NSpace>

        <NAlert v-if="rerunWarning" :type="rerunWarning.type" :show-icon="false" style="margin-bottom: 10px">
          <NText style="font-size: 12px">{{ rerunWarning.text }}</NText>
        </NAlert>

        <NAlert v-if="submittedRun" type="success" :show-icon="false" style="margin-bottom: 10px">
          <NSpace align="center" justify="space-between">
            <NText style="font-size: 12px">
              run #{{ submittedRun }} 已入队（worker 跑完后回来看闭环）
            </NText>
            <NButton size="tiny" type="primary" quaternary @click="useSubmittedAsCandidate">设为对照</NButton>
          </NSpace>
        </NAlert>

        <NText depth="3" style="display: block; font-size: 12px">
          提交前请确认 worker 在跑（`make worker`）：run 会先入队, 由 worker 逐个 case 执行。
        </NText>
      </template>

      <template #footer>
        <NSpace justify="end">
          <NButton size="small" @click="showRerun = false">关闭</NButton>
          <NButton size="small" type="primary" :loading="submitting" :disabled="!rerunForm || !canSubmit" @click="doSubmit">
            提交重跑
          </NButton>
        </NSpace>
      </template>
    </NModal>

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
