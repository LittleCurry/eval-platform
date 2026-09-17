<script setup lang="ts">
import { h, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSpace,
} from 'naive-ui'
import { createDataset, deleteDataset, listDatasets } from '../api/datasets'
import type { Dataset } from '../api/types'
import { currentProjectId, useProject } from '../composables/useProject'
import { usePermission } from '../composables/usePermission'

const router = useRouter()
const message = useMessage()
const { hasProjects, emptyHint } = useProject()
const { canWrite, canDelete } = usePermission()

const datasets = ref<Dataset[]>([])
const loading = ref(false)
const errorText = ref('')

async function load() {
  const projectId = currentProjectId.value
  if (projectId === null) {
    datasets.value = []
    return
  }
  loading.value = true
  errorText.value = ''
  try {
    datasets.value = await listDatasets(projectId)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}
onMounted(load)
// 切换项目后重新加载(M7-2): 数据集属于项目, 看串项目等于看错实验
watch(currentProjectId, load)

const showCreate = ref(false)
const createForm = ref({ name: '', description: '' })

async function submitCreate() {
  const name = createForm.value.name.trim()
  const projectId = currentProjectId.value
  if (!name) {
    message.warning('名称必填')
    return
  }
  if (projectId === null) {
    message.error('先建一个项目, 数据集要挂在项目下')
    return
  }
  try {
    await createDataset({ project_id: projectId, name, description: createForm.value.description })
    message.success('数据集已创建')
    showCreate.value = false
    createForm.value = { name: '', description: '' }
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

async function onDelete(row: Dataset) {
  if (!window.confirm(`确认删除数据集「${row.name}」及其全部用例？`)) return
  try {
    await deleteDataset(row.id)
    message.success('已删除')
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

const columns: DataTableColumns<Dataset> = [
  { title: 'ID', key: 'id', width: 70 },
  { title: '名称', key: 'name' },
  { title: '描述', key: 'description' },
  {
    title: '用例数',
    key: 'case_count',
    width: 90,
    render: (row) => String(row.case_count ?? 0),
  },
  {
    title: '操作',
    key: 'actions',
    width: 200,
    render: (row) =>
        h(NSpace, {}, {
          default: () => [
            h(NButton, { size: 'small', onClick: () => router.push(`/datasets/${row.id}`) }, { default: () => '管理用例' }),
            h(
                NButton,
                {
                  size: 'small', type: 'error', quaternary: true,
                  // 删数据集(连用例)不可逆: 只给管理员
                  disabled: !canDelete.value, onClick: () => onDelete(row),
                },
                { default: () => '删除' },
            ),
          ],
        }),
  },
]
</script>

<template>
  <NCard title="数据集">
    <template #header-extra>
      <NButton type="primary" :disabled="!canWrite" @click="showCreate = true">新建数据集</NButton>
    </template>

      <NAlert v-if="!hasProjects" type="info" :show-icon="false" style="margin-bottom: 12px">
        {{ emptyHint }}
      </NAlert>
    <NAlert v-if="errorText && datasets.length > 0" type="error" :show-icon="false" style="margin-bottom: 12px">
      {{ errorText }}
    </NAlert>

    <NDataTable
        :columns="columns"
        :data="datasets"
        :loading="loading"
        :row-key="(row: Dataset) => row.id"
        size="small"
    />

    <NModal v-model:show="showCreate">
      <NCard style="width: 480px" title="新建数据集" :bordered="false" size="huge" role="dialog">
        <NForm label-placement="left" label-width="80">
          <NFormItem label="名称">
            <NInput v-model:value="createForm.name" placeholder="如：知简CRM-条款题" />
          </NFormItem>
          <NFormItem label="描述">
            <NInput v-model:value="createForm.description" placeholder="可选" />
          </NFormItem>
          <NSpace justify="end">
            <NButton @click="showCreate = false">取消</NButton>
            <NButton type="primary" :disabled="!canWrite" @click="submitCreate">创建</NButton>
          </NSpace>
        </NForm>
      </NCard>
    </NModal>
  </NCard>
</template>