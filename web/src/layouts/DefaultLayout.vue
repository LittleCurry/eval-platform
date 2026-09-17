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
import { usePermission } from '../composables/usePermission'
import { roleLabel, roleTagType } from '../utils/session'
import { switchNotice } from '../utils/projectContext'
import { NAV_TREE, activeNavKey, visibleNav } from '../utils/navigation'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const session = useSession()
const project = useProject()
const { readOnlyHint } = usePermission()

// 菜单清单与"哪些项该隐藏"都在 utils/navigation.ts(纯数据 + 双向对照路由的测试)。
// 这里只做一件事: 把可见项翻成 naive-ui 的 MenuOption。
const menuOptions = computed<MenuOption[]>(() =>
  visibleNav(session.role.value, NAV_TREE).map((item) => ({ label: item.label, key: item.key })),
)

const currentTitle = computed(() => {
  const matched = NAV_TREE.find((item) => item.key === activeNavKey(route.name as string))
  return matched?.label ?? String(route.meta.title ?? '')
})

/**
 * 账号下拉: 邮箱与角色都放在里面。
 *
 * 为什么把角色从顶栏的标签挪进来: 顶栏一行要塞"标题 + 项目 + 角色 + 账号",
 * 角色标签虽然小, 但它是"看一眼就够"的信息 —— 放进下拉既省宽度, 又不丢信息
 * (点自己的名字就能看到自己是谁、什么权限)。
 */
const accountOptions = computed(() => [
  { key: 'who', label: `${session.user.value?.email ?? ''} · ${roleLabel(session.role.value)}`, disabled: true },
  { type: 'divider', key: 'divider' },
  { label: '退出登录', key: 'logout' },
])

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

function onSelect(key: string) {
  router.push({ name: key })
}
</script>

<template>
  <NLayout style="min-height: 100vh">
    <!--
      顶栏分两行(M7-3):
      以前"标题 + 11 个页面 + 项目切换 + 角色 + 账号"全挤在 64px 的一行里,
      加起来约 1550px —— 窗口窄于 1440px 时最后一个菜单项就被截成"用户…"。
      现在: 第一行只放"我在哪(项目)"和"我是谁", 导航独占第二行, 且窄屏横向滚动而不是截断。
    -->
    <NLayoutHeader bordered class="app-header">
      <div class="header-top">
        <NText strong class="app-title">
          Eval Platform<span class="app-subtitle"> · 评测控制台</span>
          <span v-if="currentTitle" class="app-breadcrumb">/ {{ currentTitle }}</span>
        </NText>

        <NSpace align="center" :size="8" class="header-actions">
          <!-- 项目切换(M7-2): 语料/数据集/实验/报告都挂在项目下, 顶栏要能一眼看到"现在在哪个项目" -->
          <NSelect
              v-if="session.loggedIn.value && project.hasProjects.value"
              :value="project.currentProjectId.value"
              :options="projectOptions"
              size="small"
              class="project-select"
              :consistent-menu-width="false"
              @update:value="onSelectProject"
          />
          <NButton
              v-else-if="session.loggedIn.value && project.canCreate.value"
              size="small"
              type="primary"
              @click="showCreateProject = true"
          >
            建第一个项目
          </NButton>

          <NDropdown
              v-if="session.loggedIn.value"
              :options="accountOptions"
              @select="onAccountSelect"
          >
            <NButton size="small" quaternary>
              <NTag size="tiny" :type="roleTagType(session.role.value)" :bordered="false">
                {{ roleLabel(session.role.value) }}
              </NTag>
              <span class="account-name">{{ session.user.value?.name || session.user.value?.email }}</span>
            </NButton>
          </NDropdown>
          <NButton v-else size="small" type="primary" @click="router.push('/login')">登录</NButton>
        </NSpace>
      </div>

      <div class="header-nav">
        <NMenu
            mode="horizontal"
            :value="activeNavKey(route.name as string)"
            :options="menuOptions"
            :root-indent="14"
            @update:value="onSelect"
        />
      </div>
    </NLayoutHeader>

    <NLayoutContent content-style="padding: 24px; max-width: 1200px; margin: 0 auto">
      <!-- 只读账号: 一进页面就说清"能看不能改", 而不是让他点到 403 才发现(M7-3) -->
      <NAlert
          v-if="readOnlyHint"
          type="info"
          :show-icon="false"
          style="margin-bottom: 16px"
      >
        <NText style="font-size: 12px">{{ readOnlyHint }}</NText>
      </NAlert>
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

<style scoped>
.app-header {
  display: block;
  padding: 0 24px 4px;
}

/* 第一行: "我在哪个项目 / 我是谁" */
.header-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  height: 54px;
}

.app-title {
  white-space: nowrap;
  flex-shrink: 0;
}

.app-breadcrumb {
  margin-left: 4px;
  opacity: 0.45;
  font-weight: 400;
}

.header-actions {
  flex-shrink: 0;
}

/* 项目名可能很长: 固定宽度 + 不撑破顶栏(完整名字在下拉里看) */
.project-select {
  width: 200px;
}

.account-name {
  margin-left: 6px;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  display: inline-block;
  vertical-align: bottom;
}

/* 第二行: 导航独占一行, 窄屏横向滚动而不是把标签截断成"用户…" */
.header-nav {
  display: flex;
  align-items: center;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.header-nav::-webkit-scrollbar {
  display: none;
}

/* 收紧菜单项内边距: naive-ui 默认左右各 16px, 11 项就是 350px 的空白 */
.header-nav :deep(.n-menu-item-content) {
  padding-left: 10px;
  padding-right: 10px;
}

/*
  关键一条: 不许菜单项被压缩。
  naive-ui 在横向空间不足时是**压缩每一项**并把文字截成"用户…", 而不是溢出 ——
  那样上面那行 overflow-x: auto 永远不会触发。钉住自然宽度后, 窄屏才会真正变成
  "可横向滚动", 标签始终完整。
*/
.header-nav :deep(.n-menu-item) {
  flex-shrink: 0;
}

/*
  溢出必须由**菜单自己**滚动: naive-ui 的 .n-menu 自带 overflow: hidden,
  所以容器上的 overflow-x: auto 永远等不到溢出(实测 menuScroll=808 > client=472,
  而外层容器的 scrollWidth 仍是 472)。放在菜单上才真正可滚。
*/
.header-nav :deep(.n-menu) {
  flex-wrap: nowrap;
  overflow-x: auto;
  scrollbar-width: none;
}

.header-nav :deep(.n-menu)::-webkit-scrollbar {
  display: none;
}

.header-nav :deep(.n-menu-item-content) {
  min-width: max-content;
}

@media (max-width: 1100px) {
  .app-subtitle {
    display: none;
  }
}

@media (max-width: 820px) {
  .app-breadcrumb {
    display: none;
  }

  .project-select {
    width: 150px;
  }
}
</style>
