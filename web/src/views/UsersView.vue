<script setup lang="ts">
import { h, onMounted, ref } from 'vue'
import { useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NInput,
  NModal,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { createUser, deleteUser, listUsers, updateUser } from '../api/auth'
import type { Role, UserAccount } from '../api/types'
import { formatDateTime } from '../utils/format'
import { roleLabel, roleTagType } from '../utils/session'
import { useSession } from '../composables/useSession'

const message = useMessage()
const session = useSession()

const users = ref<UserAccount[]>([])
const loading = ref(false)
const errorText = ref('')

const showCreate = ref(false)
const draft = ref<{ email: string; name: string; password: string; role: Role }>({
  email: '', name: '', password: '', role: 'viewer',
})
const creating = ref(false)

const roleOptions = [
  { label: '只读（看报告，不能改任何东西）', value: 'viewer' },
  { label: '编辑者（能跑实验、能标注）', value: 'editor' },
  { label: '管理员（还能删数据、管账号）', value: 'admin' },
]

async function load() {
  loading.value = true
  errorText.value = ''
  try {
    users.value = await listUsers()
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function submitCreate() {
  if (!draft.value.email.trim() || !draft.value.password) {
    message.error('邮箱与口令必填')
    return
  }
  creating.value = true
  try {
    await createUser({
      email: draft.value.email.trim(),
      name: draft.value.name.trim(),
      password: draft.value.password,
      role: draft.value.role,
    })
    message.success('账号已创建')
    showCreate.value = false
    draft.value = { email: '', name: '', password: '', role: 'viewer' }
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    creating.value = false
  }
}

/** 改角色: 成功后会同步本地会话(改的可能是自己, 虽然服务端会挡住自我降级)。 */
async function changeRole(user: UserAccount, role: Role) {
  try {
    const updated = await updateUser(user.id, { role })
    message.success(`${updated.email} 的角色已改为${roleLabel(updated.role)}`)
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
    await load() // 失败时回滚界面上的开关
  }
}

async function toggleDisabled(user: UserAccount, disabled: boolean) {
  try {
    await updateUser(user.id, { disabled })
    message.success(disabled ? '已停用(其登录状态立刻失效)' : '已恢复')
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
    await load()
  }
}

async function resetPassword(user: UserAccount) {
  const value = window.prompt(`给 ${user.email} 设置新口令（至少 8 位）`)
  if (!value) return
  try {
    await updateUser(user.id, { password: value })
    message.success('口令已重置')
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

async function remove(user: UserAccount) {
  if (!window.confirm(`删除账号 ${user.email}？\n\n历史记录里 created_by 会变成空值（不删数据）。`)) return
  try {
    await deleteUser(user.id)
    message.success('已删除')
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

onMounted(load)

const columns: DataTableColumns<UserAccount> = [
  { title: '邮箱', key: 'email', minWidth: 200 },
  { title: '名字', key: 'name', width: 120, render: (row) => row.name || '—' },
  {
    title: '角色',
    key: 'role',
    width: 220,
    render: (row) =>
        h(NSelect, {
          value: row.role,
          size: 'small',
          options: roleOptions,
          disabled: row.id === session.user.value?.id, // 自己改自己会被服务端挡(409), 不如先禁掉
          onUpdateValue: (value: string) => void changeRole(row, value as Role),
        }),
  },
  {
    title: '状态',
    key: 'disabled',
    width: 150,
    render: (row) =>
        h(NSpace, { size: 6, align: 'center' }, {
          default: () => [
            h(NTag, { size: 'small', type: row.disabled ? 'warning' : 'success' },
                { default: () => (row.disabled ? '已停用' : '正常') }),
            h(NSwitch, {
              value: !row.disabled,
              size: 'small',
              disabled: row.id === session.user.value?.id,
              onUpdateValue: (value: boolean) => void toggleDisabled(row, !value),
            }),
          ],
        }),
  },
  {
    title: '最近登录',
    key: 'last_login_at',
    width: 170,
    render: (row) => (row.last_login_at ? formatDateTime(row.last_login_at) : '从未登录'),
  },
  {
    title: '角色说明',
    key: 'roleTag',
    width: 100,
    render: (row) => h(NTag, { size: 'small', type: roleTagType(row.role) },
        { default: () => roleLabel(row.role) }),
  },
  {
    title: '操作',
    key: 'action',
    width: 170,
    fixed: 'right',
    render: (row) =>
        h(NSpace, { size: 4 }, {
          default: () => [
            h(NButton, { size: 'tiny', quaternary: true, onClick: () => void resetPassword(row) },
                { default: () => '重置口令' }),
            h(NButton, {
              size: 'tiny', quaternary: true, type: 'error',
              disabled: row.id === session.user.value?.id,
              onClick: () => void remove(row),
            }, { default: () => '删除' }),
          ],
        }),
  },
]
</script>

<template>
  <div>
    <NCard title="用户管理" style="margin-bottom: 16px">
      <template #header-extra>
        <NSpace align="center">
          <NButton size="small" type="primary" @click="showCreate = true">添加账号</NButton>
          <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        </NSpace>
      </template>

      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
        {{ errorText }}
      </NAlert>

      <NText depth="3" style="display: block; margin-bottom: 12px; font-size: 12px">
        角色能力：只读 = 看报告/对比/标注结果；编辑者 = 还能跑实验、标注、打分、存配置模板；
        管理员 = 还能删数据、管账号。改角色后对方**下一次刷新**即生效（停用是立刻生效）。
      </NText>

      <NDataTable
          :columns="columns"
          :data="users"
          :loading="loading"
          :row-key="(row: UserAccount) => String(row.id)"
          :scroll-x="1100"
          size="small"
      />
    </NCard>

    <NModal
        v-model:show="showCreate"
        preset="card"
        title="添加账号"
        style="width: 480px"
    >
      <NSpace vertical :size="12">
        <NInput v-model:value="draft.email" placeholder="邮箱（登录名）" />
        <NInput v-model:value="draft.name" placeholder="显示名（可留空）" />
        <NInput
            v-model:value="draft.password"
            type="password"
            show-password-on="click"
            placeholder="初始口令（至少 8 位）"
        />
        <NSelect v-model:value="draft.role" :options="roleOptions" />
        <NText depth="3" style="font-size: 12px">
          默认给「只读」：加人时忘了选角色，不该变成给出管理员权限。
        </NText>
      </NSpace>
      <template #footer>
        <NSpace justify="end">
          <NButton size="small" @click="showCreate = false">取消</NButton>
          <NButton size="small" type="primary" :loading="creating" @click="submitCreate">创建</NButton>
        </NSpace>
      </template>
    </NModal>
  </div>
</template>
