<script setup lang="ts">
import { h, onMounted, ref } from 'vue'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSpace,
  NTag,
} from 'naive-ui'
import {
  createCorpus,
  deleteCorpus,
  deleteDocument,
  listCorpora,
  listDocuments,
  uploadDocuments,
  type DocumentUploadItem,
} from '../api/corpora'
import type { Corpus, Document } from '../api/types'
import { currentProjectId } from '../composables/useProject'

const message = useMessage()

const corpora = ref<Corpus[]>([])
const loading = ref(false)
const errorText = ref('')

async function load() {
  loading.value = true
  errorText.value = ''
  try {
    corpora.value = await listCorpora(currentProjectId.value)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- 新建语料库 ----
const showCreate = ref(false)
const createForm = ref({ name: '', source_type: 'manual' })

async function submitCreate() {
  try {
    await createCorpus({ project_id: currentProjectId.value, ...createForm.value })
    message.success('语料库已创建')
    showCreate.value = false
    createForm.value = { name: '', source_type: 'manual' }
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

async function onDeleteCorpus(row: Corpus) {
  if (!window.confirm(`确认删除语料库「${row.name}」及其全部文档？`)) return
  try {
    await deleteCorpus(row.id)
    message.success('已删除')
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

// ---- 文档抽屉 ----
const drawerOpen = ref(false)
const drawerCorpus = ref<Corpus | null>(null)
const documents = ref<Document[]>([])
const docsLoading = ref(false)

async function openDocuments(row: Corpus) {
  drawerCorpus.value = row
  documents.value = []
  drawerOpen.value = true
  docsLoading.value = true
  try {
    documents.value = await listDocuments(row.id)
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    docsLoading.value = false
  }
}

async function onDeleteDocument(id: number) {
  if (!window.confirm('确认删除该文档？')) return
  try {
    await deleteDocument(id)
    message.success('已删除')
    if (drawerCorpus.value) await openDocuments(drawerCorpus.value)
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

// ---- 上传文档 ----
const showUpload = ref(false)
const uploadForm = ref({ doc_id: '', title: '', raw_text: '' })

async function submitUpload() {
  if (!drawerCorpus.value) return
  const item: DocumentUploadItem = {
    doc_id: uploadForm.value.doc_id.trim(),
    title: uploadForm.value.title.trim(),
    raw_text: uploadForm.value.raw_text,
  }
  if (!item.doc_id || !item.title || !item.raw_text.trim()) {
    message.warning('doc_id / title / raw_text 均为必填')
    return
  }
  try {
    const res = await uploadDocuments(drawerCorpus.value.id, [item])
    message.success(`已上传 ${res.inserted} 篇`)
    showUpload.value = false
    uploadForm.value = { doc_id: '', title: '', raw_text: '' }
    await openDocuments(drawerCorpus.value)
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

const corpusColumns: DataTableColumns<Corpus> = [
  { title: 'ID', key: 'id', width: 70 },
  { title: '名称', key: 'name' },
  {
    title: '类型',
    key: 'source_type',
    width: 110,
    render: (row) => h(NTag, { size: 'small' }, { default: () => row.source_type }),
  },
  {
    title: '操作',
    key: 'actions',
    width: 220,
    render: (row) =>
        h(NSpace, {}, {
          default: () => [
            h(NButton, { size: 'small', onClick: () => openDocuments(row) }, { default: () => '文档' }),
            h(
                NButton,
                { size: 'small', type: 'error', quaternary: true, onClick: () => onDeleteCorpus(row) },
                { default: () => '删除' },
            ),
          ],
        }),
  },
]

const docColumns: DataTableColumns<Document> = [
  { title: 'doc_id', key: 'doc_id', width: 90 },
  { title: '标题', key: 'title' },
  { title: 'meta', key: 'meta', render: (row) => JSON.stringify(row.meta ?? {}) },
  {
    title: '操作',
    key: 'actions',
    width: 90,
    render: (row) =>
        h(
            NButton,
            { size: 'small', type: 'error', quaternary: true, onClick: () => onDeleteDocument(row.id) },
            { default: () => '删除' },
        ),
  },
]
</script>

<template>
  <NCard title="语料库">
    <template #header-extra>
      <NButton type="primary" @click="showCreate = true">新建语料库</NButton>
    </template>

    <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
      {{ errorText }}
    </NAlert>

    <NDataTable
        :columns="corpusColumns"
        :data="corpora"
        :loading="loading"
        :row-key="(row: Corpus) => row.id"
        size="small"
    />

    <NModal v-model:show="showCreate">
      <NCard style="width: 480px" title="新建语料库" :bordered="false" size="huge" role="dialog">
        <NForm label-placement="left" label-width="90">
          <NFormItem label="名称">
            <NInput v-model:value="createForm.name" placeholder="如：产品帮助文档" />
          </NFormItem>
          <NFormItem label="类型">
            <NSelect
                v-model:value="createForm.source_type"
                :options="[
                { label: '手工录入', value: 'manual' },
                { label: '上传导入', value: 'upload' },
                { label: '外部接入', value: 'external' },
              ]"
            />
          </NFormItem>
          <NSpace justify="end">
            <NButton @click="showCreate = false">取消</NButton>
            <NButton type="primary" @click="submitCreate">创建</NButton>
          </NSpace>
        </NForm>
      </NCard>
    </NModal>

    <NDrawer v-model:show="drawerOpen" :width="720">
      <NDrawerContent :title="drawerCorpus ? `文档列表 · ${drawerCorpus.name}` : '文档列表'">
        <NButton type="primary" size="small" style="margin-bottom: 12px" @click="showUpload = true">
          上传文档
        </NButton>
        <NDataTable
            :columns="docColumns"
            :data="documents"
            :loading="docsLoading"
            :row-key="(row: Document) => row.id"
            size="small"
        />

        <NModal v-model:show="showUpload">
          <NCard style="width: 560px" title="上传文档" :bordered="false" size="huge" role="dialog">
            <NForm label-placement="left" label-width="80">
              <NFormItem label="doc_id">
                <NInput v-model:value="uploadForm.doc_id" placeholder="语料中唯一, 如 A01" />
              </NFormItem>
              <NFormItem label="标题">
                <NInput v-model:value="uploadForm.title" placeholder="文档标题" />
              </NFormItem>
              <NFormItem label="正文">
                <NInput
                    v-model:value="uploadForm.raw_text"
                    type="textarea"
                    :rows="10"
                    placeholder="粘贴 markdown/文本正文"
                />
              </NFormItem>
              <NSpace justify="end">
                <NButton @click="showUpload = false">取消</NButton>
                <NButton type="primary" @click="submitUpload">上传</NButton>
              </NSpace>
            </NForm>
          </NCard>
        </NModal>
      </NDrawerContent>
    </NDrawer>
  </NCard>
</template>