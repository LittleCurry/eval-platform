<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDivider,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { listCorpora } from '../api/corpora'
import { listDatasets } from '../api/datasets'
import {
  createPipelineProfile,
  deletePipelineProfile,
  listPipelineProfiles,
  previewPipeline,
  updatePipelineProfile,
} from '../api/pipelineProfiles'
import type { PipelinePreview, PipelineProfile } from '../api/types'
import { currentProjectId } from '../composables/useProject'
import { formatDateTime, shortHash } from '../utils/format'
import {
  configToForm,
  emptyProfileForm,
  formToConfig,
  formToPreviewPayload,
  profileSummary,
  validateProfileForm,
  type ProfileForm,
} from '../utils/profile'

const message = useMessage()

const profiles = ref<PipelineProfile[]>([])
const loading = ref(false)
const errorText = ref('')

// 预览需要 corpus/dataset: 指纹里含数据来源(D7), 少了它们算出来的 hash 没有意义
const corpora = ref<{ label: string; value: number }[]>([])
const datasets = ref<{ label: string; value: number }[]>([])
const previewCorpusId = ref<number | null>(null)
const previewDatasetId = ref<number | null>(null)
const preview = ref<PipelinePreview | null>(null)
const previewing = ref(false)

async function load() {
  loading.value = true
  errorText.value = ''
  try {
    profiles.value = await listPipelineProfiles(currentProjectId.value)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function loadTargets() {
  try {
    const [corpusList, datasetList] = await Promise.all([
      listCorpora(currentProjectId.value),
      listDatasets(currentProjectId.value),
    ])
    corpora.value = corpusList.map((item) => ({ label: `#${item.id} ${item.name}`, value: item.id }))
    datasets.value = datasetList.map((item) => ({ label: `#${item.id} ${item.name}`, value: item.id }))
    previewCorpusId.value = corpora.value[0]?.value ?? null
    previewDatasetId.value = datasets.value[0]?.value ?? null
  } catch {
    // 预览是锦上添花: 拉不到列表就不显示预览区, 不影响模板管理本身
  }
}

onMounted(async () => {
  await Promise.all([load(), loadTargets()])
})

// ---- 新建 / 编辑 ----

const showModal = ref(false)
const editingId = ref<number | null>(null)
const saving = ref(false)
const formError = ref('')
const form = ref<ProfileForm>(emptyProfileForm())
const meta = ref({ name: '', description: '' })

function openCreate() {
  editingId.value = null
  form.value = emptyProfileForm()
  meta.value = { name: '', description: '' }
  formError.value = ''
  showModal.value = true
}

function openEdit(row: PipelineProfile) {
  editingId.value = row.id
  form.value = configToForm(row.config)
  meta.value = { name: row.name, description: row.description }
  formError.value = ''
  showModal.value = true
}

async function submit() {
  if (!meta.value.name.trim()) {
    formError.value = '名称必填'
    return
  }
  const invalid = validateProfileForm(form.value)
  if (invalid) {
    formError.value = invalid
    return
  }
  saving.value = true
  formError.value = ''
  try {
    const config = formToConfig(form.value)
    if (editingId.value === null) {
      await createPipelineProfile({
        project_id: currentProjectId.value,
        name: meta.value.name.trim(),
        description: meta.value.description.trim(),
        config,
      })
      message.success('模板已创建')
    } else {
      await updatePipelineProfile(editingId.value, {
        name: meta.value.name.trim(),
        description: meta.value.description.trim(),
        config,
      })
      message.success('模板已更新')
    }
    showModal.value = false
    await load()
  } catch (err) {
    formError.value = err instanceof Error ? err.message : String(err)
  } finally {
    saving.value = false
  }
}

async function onDelete(row: PipelineProfile) {
  try {
    await deletePipelineProfile(row.id)
    message.success('模板已删除')
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

// ---- 指纹预览 ----

const canPreview = computed(() => Boolean(previewCorpusId.value && previewDatasetId.value))

async function runPreview() {
  const payload = formToPreviewPayload(form.value, previewCorpusId.value, previewDatasetId.value)
  if (!payload) {
    message.warning('请先选择语料库与评测集')
    return
  }
  previewing.value = true
  try {
    preview.value = await previewPipeline(payload)
  } catch (err) {
    preview.value = null
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    previewing.value = false
  }
}

const columns: DataTableColumns<PipelineProfile> = [
  { title: 'ID', key: 'id', width: 70 },
  { title: '名称', key: 'name', width: 180 },
  { title: '描述', key: 'description', width: 220, ellipsis: { tooltip: true } },
  {
    title: '配置摘要',
    key: 'summary',
    width: 260,
    render: (row) => profileSummary(row.config),
  },
  {
    title: '阶段',
    key: 'stages',
    width: 140,
    render: (row) =>
        h(NSpace, { size: 4 }, {
          default: () => [
            row.config?.generation
                ? h(NTag, { size: 'small', type: 'info' }, { default: () => '生成' })
                : null,
            row.config?.judge
                ? h(NTag, { size: 'small', type: 'warning' }, { default: () => '判定' })
                : null,
            !row.config?.generation && !row.config?.judge
                ? h(NText, { depth: 3 }, { default: () => '仅检索' })
                : null,
          ],
        }),
  },
  { title: '更新时间', key: 'updated_at', width: 150, render: (row) => formatDateTime(row.updated_at) },
  {
    title: '操作',
    key: 'actions',
    width: 150,
    render: (row) =>
        h(NSpace, { size: 4 }, {
          default: () => [
            h(NButton, { size: 'small', quaternary: true, onClick: () => openEdit(row) }, { default: () => '编辑' }),
            h(NButton, {
              size: 'small',
              quaternary: true,
              type: 'error',
              onClick: () => onDelete(row),
            }, { default: () => '删除' }),
          ],
        }),
  },
]
</script>

<template>
  <div>
    <NCard title="配置模板" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace>
          <NButton size="small" :loading="loading" @click="load">刷新</NButton>
          <NButton size="small" type="primary" @click="openCreate">新建模板</NButton>
        </NSpace>
      </template>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">{{ errorText }}</NAlert>
      <NText depth="3" style="display: block; margin-bottom: 12px; font-size: 12px">
        模板只固化"提交请求里的旋钮"（切分 / top_k / 是否启用生成与判定），
        语料库与评测集在提交时才选 —— 所以模板到提交请求是一一对应，不会有存了却不生效的字段。
      </NText>

      <NDataTable
          :columns="columns"
          :data="profiles"
          :loading="loading"
          :row-key="(row: PipelineProfile) => row.id"
          :scroll-x="1170"
          size="small"
      />
    </NCard>

    <NCard title="提交前看指纹" style="margin-bottom: 16px">
      <NText depth="3" style="display: block; margin-bottom: 10px; font-size: 12px">
        选好语料库与评测集 → 算出**将来会落库的那个 config_hash**。同配置同 hash：
        可以用它确认"这次是新实验，还是又跑了一遍老配置"（D7）。
      </NText>
      <NSpace align="center" :size="12">
        <NSelect v-model:value="previewCorpusId" :options="corpora" placeholder="语料库" style="width: 220px" />
        <NSelect v-model:value="previewDatasetId" :options="datasets" placeholder="评测集" style="width: 220px" />
        <NButton size="small" :disabled="!canPreview" :loading="previewing" @click="runPreview">
          用表单里的配置预览
        </NButton>
        <NButton size="small" quaternary @click="openCreate()">用新配置试算</NButton>
      </NSpace>

      <NDescriptions v-if="preview" :column="1" label-placement="left" bordered size="small" style="margin-top: 12px">
        <NDescriptionsItem label="config_hash">
          <NText code>{{ preview.config_hash }}</NText>
        </NDescriptionsItem>
        <NDescriptionsItem label="chunking_hash">
          <NText code>{{ shortHash(preview.chunking_hash, 16) }}…</NText>
        </NDescriptionsItem>
        <NDescriptionsItem label="Qdrant 集合">{{ preview.collection }}</NDescriptionsItem>
        <NDescriptionsItem label="启用阶段">
          {{ preview.generation_enabled ? '生成' : '仅检索' }}{{ preview.judge_enabled ? ' + 判定' : '' }}
        </NDescriptionsItem>
      </NDescriptions>
    </NCard>

    <NModal v-model:show="showModal" preset="card" :title="editingId === null ? '新建模板' : `编辑模板 #${editingId}`" style="width: 720px">
      <NForm label-placement="left" label-width="110">
        <NFormItem label="名称">
          <NInput v-model:value="meta.name" placeholder="例如：基线 k=5（仅检索）" />
        </NFormItem>
        <NFormItem label="描述">
          <NInput v-model:value="meta.description" placeholder="这组配置想验证什么？" />
        </NFormItem>
      </NForm>

      <NCollapse :default-expanded-names="['chunking', 'retrieval']">
        <NCollapseItem title="切分" name="chunking">
          <NSpace :size="12" align="center">
            <NSelect
                v-model:value="form.chunking.strategy"
                :options="[{ label: 'headings', value: 'headings' }, { label: 'chars', value: 'chars' }]"
                style="width: 140px"
            />
            <NInputNumber v-model:value="form.chunking.chunk_size" :min="100" :step="50" style="width: 140px">
              <template #prefix>size</template>
            </NInputNumber>
            <NInputNumber v-model:value="form.chunking.overlap" :min="0" :step="10" style="width: 140px">
              <template #prefix>overlap</template>
            </NInputNumber>
            <NInputNumber v-model:value="form.chunking.min_chars" :min="0" :step="10" style="width: 140px">
              <template #prefix>min</template>
            </NInputNumber>
          </NSpace>
        </NCollapseItem>

        <NCollapseItem title="检索" name="retrieval">
          <NSpace align="center" :size="12">
            <NInputNumber v-model:value="form.topK" :min="1" :max="50" style="width: 140px">
              <template #prefix>top_k</template>
            </NInputNumber>
            <NText depth="3" style="font-size: 12px">上限 50（与提交接口一致）</NText>
          </NSpace>
        </NCollapseItem>

        <NCollapseItem name="generation">
          <template #header>
            <NSpace align="center" :size="8">
              <NSwitch v-model:value="form.generationEnabled" size="small" />
              <span>生成（不启用则只跑检索）</span>
            </NSpace>
          </template>
          <NSpace :size="12" align="center" style="margin-bottom: 8px">
            <NInput v-model:value="form.generation.model" placeholder="model" style="width: 240px" />
            <NInput v-model:value="form.generation.prompt_id" placeholder="prompt_id" style="width: 160px" />
          </NSpace>
          <NSpace :size="12" align="center">
            <NInputNumber v-model:value="form.generation.temperature" :min="0" :max="2" :step="0.1" style="width: 150px">
              <template #prefix>temp</template>
            </NInputNumber>
            <NInputNumber v-model:value="form.generation.max_tokens" :min="16" :step="128" style="width: 150px">
              <template #prefix>max_tok</template>
            </NInputNumber>
            <NInputNumber v-model:value="form.generation.max_context_chars" :min="200" :step="500" style="width: 170px">
              <template #prefix>ctx</template>
            </NInputNumber>
          </NSpace>
        </NCollapseItem>

        <NCollapseItem name="judge">
          <template #header>
            <NSpace align="center" :size="8">
              <NSwitch v-model:value="form.judgeEnabled" size="small" :disabled="!form.generationEnabled" />
              <span>判定（claim 核查 + rubric 打分；需要先启用生成）</span>
            </NSpace>
          </template>
          <NSpace :size="12" align="center" style="margin-bottom: 8px">
            <NInput v-model:value="form.judge.model" placeholder="judge model" style="width: 240px" />
            <NInput v-model:value="form.judge.claims_prompt_id" placeholder="claims_prompt_id" style="width: 200px" />
          </NSpace>
          <NSpace :size="12" align="center" style="margin-bottom: 8px">
            <NInput v-model:value="form.judge.rubric_prompt_id" placeholder="rubric_prompt_id" style="width: 200px" />
            <NSpace align="center" :size="6">
              <NText depth="3" style="font-size: 12px">启用 rubric</NText>
              <NSwitch v-model:value="form.judge.enable_rubric" size="small" />
            </NSpace>
            <NInputNumber v-model:value="form.judge.max_claims" :min="1" :max="50" style="width: 150px">
              <template #prefix>max_clm</template>
            </NInputNumber>
          </NSpace>
          <NText depth="3" style="font-size: 12px">
            rubric v2 的硬规则是"有 unsupported 断言则 helpfulness ≤ 2"，改 prompt 版本时注意配合（见 D16/M4-2）。
          </NText>
        </NCollapseItem>
      </NCollapse>

      <NDivider />

      <NAlert v-if="formError" type="error" :show-icon="false" style="margin-bottom: 12px">{{ formError }}</NAlert>
      <NSpace justify="end">
        <NButton @click="showModal = false">取消</NButton>
        <NButton :loading="saving" @click="submit">保存</NButton>
      </NSpace>
    </NModal>
  </div>
</template>