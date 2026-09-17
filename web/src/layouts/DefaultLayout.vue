<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NDropdown,
  NInput,
  NLayout,
  NLayoutHeader,
  NLayoutContent,
  NMenu,
  NModal,
  NSelect,
  NSpace,
  NTag,
  NText,
  useMessage,
  type MenuOption,
} from 'naive-ui'
import { initSession, signOut, useSession } from '../composables/useSession'
import { useProject } from '../composables/useProject'
import { roleLabel, roleTagType, visibleMenuKeys } from '../utils/session'
import { switchNotice } from '../utils/projectContext'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const session = useSession()
const project = useProject()

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

// ---- 项目切换(M7-2) ----

const projectOptions = computed(() =>
  project.projects.value.map((item) => ({
    label: project.label(item),
    value: item.id,
  })),
)

const showCreateProject = ref(false)
const projectDraft = ref({ name: '', description: '' })
const creatingProject = ref(false)

function onSelectProject(id: number | null) {
  const before = project.currentProject.value
  project.select(id)
  const after = project.projects.value.find((item) => item.id === id)
  if (before && after && before.id !== after.id) {
    // 明确提示"你看的东西换了": 切项目后列表/报告全变, 不说清很容易看串
    message.info(switchNotice(project.label(before), project.label(after)))
  }
}

async function submitProject() {
  const name = projectDraft.value.name.trim()
  if (!name) {
    message.warning('项目名必填')
    return
  }
  creatingProject.value = true
  try {
    const created = await project.create(name, projectDraft.value.description.trim())
    message.success(`项目 ${created.name} 已创建并切换过去`)
    showCreateProject.value = false
    projectDraft.value = { name: '', description: '' }
    // 新项目是空的: 引导用户先去建语料库/数据集
    router.push('/corpora')
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    creatingProject.value = false
  }
}

onMounted(async () => {
  // 直接打开某个页面(比如从书签进来)时, 布局自己要保证会话已确认
  if (!session.ready.value) await initSession()
  if (session.loggedIn.value) {
    await project.load()
    if (project.fellBack.value) {
      // 记住的项目不在了(被删/换环境): 回退到第一个, 但必须说一声
      message.warning('上次选择的项目已不存在, 已切回第一个项目')
    }
  }
})

watch(() => session.loggedIn.value, async (loggedIn) => {
  // 登录后才有项目可拉; 退出时清掉, 免得下一个人看到上一个人的项目名
  if (loggedIn) await project.load()
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
        <!-- 项目切换(M7-2): 语料/数据集/实验/报告都挂在项目下, 顶栏必须能一眼看到"现在在哪个项目" -->
        <template v-if="session.loggedIn.value && project.hasProjects.value">
          <NSelect
              :value="project.currentProjectId.value"
              :options="projectOptions"
              size="small"
              style="width: 220px"
              @update:value="onSelectProject"
          />
        </template>
        <NButton
            v-else-if="session.loggedIn.value && project.canCreate.value"
            size="small"
            type="primary"
            @click="showCreateProject = true"
        >
          建第一个项目
        </NButton>
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

    <NModal
        v-model:show="showCreateProject"
        preset="card"
        title="新建项目"
        style="width: 460px"
    >
      <NAlert type="info" :show-icon="false" style="margin-bottom: 12px">
        <NText style="font-size: 12px">
          项目是数据隔离与归属的单位：语料库、数据集、实验与报告都挂在项目下。
          新项目是空的，建完先去「语料库」上传资料。
        </NText>
      </NAlert>
      <NSpace vertical :size="12">
        <NInput v-model:value="projectDraft.name" placeholder="项目名（唯一，比如：知简CRM评测）" />
        <NInput v-model:value="projectDraft.description" placeholder="描述（可留空）" />
      </NSpace>
      <template #footer>
        <NSpace justify="end">
          <NButton size="small" @click="showCreateProject = false">取消</NButton>
          <NButton size="small" type="primary" :loading="creatingProject" @click="submitProject">
            创建并进入
          </NButton>
        </NSpace>
      </template>
    </NModal>
  </NLayout>
</template>