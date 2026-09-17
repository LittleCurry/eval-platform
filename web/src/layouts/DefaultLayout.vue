<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'
import {
  NButton,
  NDropdown,
  NLayout,
  NLayoutHeader,
  NLayoutContent,
  NMenu,
  NSpace,
  NTag,
  NText,
  type MenuOption,
} from 'naive-ui'
import { initSession, signOut, useSession } from '../composables/useSession'
import { roleLabel, roleTagType, visibleMenuKeys } from '../utils/session'

const route = useRoute()
const router = useRouter()
const session = useSession()

/**
 * 菜单 = 全部入口 + 每一项的最低角色。
 *
 * minRole 的取舍: 只给"点进去必然被拦"的页面(用户管理)设门槛; 业务页面全部保留 ——
 * 只读账号也要能看报告/对比/标注结果, 那些才是他来这个系统要做的事。
 */
const allMenu: (MenuOption & { minRole?: string })[] = [
  { label: '概览', key: 'home' },
  { label: '语料库', key: 'corpora' },
  { label: '数据集', key: 'datasets' },
  { label: '运行报告', key: 'runs' },
  { label: '标注工作台', key: 'annotations' },
  { label: '金标打分', key: 'gold' },
  { label: 'Judge 校准', key: 'calibration' },
  { label: '标注闭环', key: 'closure' },
  { label: '配置模板', key: 'profiles' },
  { label: 'A/B 对比', key: 'compare' },
  { label: '用户管理', key: 'users', minRole: 'admin' },
]

const menuOptions = computed<MenuOption[]>(() => {
  const allowed = new Set(visibleMenuKeys(session.role.value, allMenu.map((item) => ({
    key: String(item.key),
    minRole: item.minRole,
  }))))
  return allMenu.filter((item) => allowed.has(String(item.key)))
})

const accountOptions = [
  { label: '退出登录', key: 'logout' },
]

async function onAccountSelect(key: string) {
  if (key !== 'logout') return
  await signOut()
  router.push({ path: '/login' })
}

onMounted(async () => {
  // 直接打开某个页面(比如从书签进来)时, 布局自己要保证会话已确认
  if (!session.ready.value) await initSession()
})

const activeKey = computed(() => {
  const name = String(route.name ?? '')
  if (name === 'dataset-detail') return 'datasets'
  if (name === 'run-detail') return 'runs'
  return name
})

function onSelect(key: string) {
  router.push({ name: key })
}
</script>

<template>
  <NLayout style="min-height: 100vh">
    <NLayoutHeader bordered style="display: flex; align-items: center; padding: 0 24px">
      <NText strong style="margin-right: 32px">Eval Platform · 评测控制台</NText>
      <NMenu
          mode="horizontal"
          :value="activeKey"
          :options="menuOptions"
          style="flex: 1"
          @update:value="onSelect"
      />
      <NSpace align="center" :size="8">
        <NTag v-if="session.loggedIn.value" size="small" :type="roleTagType(session.role.value)">
          {{ roleLabel(session.role.value) }}
        </NTag>
        <NDropdown
            v-if="session.loggedIn.value"
            :options="accountOptions"
            @select="onAccountSelect"
        >
          <NButton size="small" quaternary>
            {{ session.user.value?.name || session.user.value?.email }}
          </NButton>
        </NDropdown>
        <NButton v-else size="small" type="primary" @click="router.push('/login')">登录</NButton>
      </NSpace>
    </NLayoutHeader>
    <NLayoutContent content-style="padding: 24px; max-width: 1200px; margin: 0 auto">
      <RouterView />
    </NLayoutContent>
  </NLayout>
</template>