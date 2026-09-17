// 项目上下文(M7-2)。
//
// 为什么要有它: M1 那会儿没有登录体系, 整个前端写死"项目 1"
// (`export const currentProjectId = ref(1)`)。接入登录与多项目后, 那行代码会变成
// 最危险的一处 —— **看起来正常, 实际把所有人的数据都往项目 1 里塞**。
//
// 现在的语义:
//   * `currentProjectId` 仍是响应式 ref, 老视图 `listCorpora(currentProjectId.value)` 照旧能用;
//   * 但它的值来自真实项目列表 + 用户上次的选择(localStorage), 不再是常量;
//   * 页面切换项目后要**重新加载**(各视图 watch 它), 否则界面还停在旧项目的数据上。

import { computed, readonly, ref } from 'vue'
import { createProject, listProjects } from '../api/projects'
import type { Project } from '../api/types'
import {
    PROJECT_STORAGE_KEY,
    emptyProjectHint,
    ownerText,
    parseStoredProjectId,
    projectLabel,
    resolveCurrentProject,
} from '../utils/projectContext'
import { can } from '../utils/session'
import { useSession } from './useSession'

/** 当前项目 id(null = 一个项目都没有)。保留这个名字, 老视图不用改。 */
export const currentProjectId = ref<number | null>(null)

const projects = ref<Project[]>([])
const loading = ref(false)
const errorText = ref('')
/** 是否发生过"记住的项目没了"的回退 —— 布局会据此提示一次, 免得用户看串项目。 */
const fellBack = ref(false)

export function useProject() {
    const session = useSession()
    return {
        projects: readonly(projects),
        currentProjectId,
        currentProject: computed(() =>
            projects.value.find((item) => item.id === currentProjectId.value) ?? null),
        loading: readonly(loading),
        errorText: readonly(errorText),
        fellBack: readonly(fellBack),
        hasProjects: computed(() => projects.value.length > 0),
        canCreate: computed(() => can(session.role.value, 'admin')),
        emptyHint: computed(() => emptyProjectHint(can(session.role.value, 'admin'))),
        label: projectLabel,
        owner: ownerText,
        load,
        select: selectProject,
        create,
    }
}

/**
 * 拉项目列表并决定当前项目。
 *
 * 回退策略在 utils/projectContext.ts 里(有测试): 记住的还在就用它; 没了就回退到第一个,
 * 并且**把回退标记出来**, 让界面能提示一次 —— 静默换项目最坑。
 */
async function load() {
    loading.value = true
    errorText.value = ''
    try {
        projects.value = await listProjects()
        const resolution = resolveCurrentProject(
            projects.value,
            parseStoredProjectId(window.localStorage.getItem(PROJECT_STORAGE_KEY)),
        )
        fellBack.value = resolution.fallback
        applyCurrent(resolution.projectId)
    } catch (err) {
        errorText.value = err instanceof Error ? err.message : String(err)
    } finally {
        loading.value = false
    }
}

/** 手动切换项目。 */
export function selectProject(id: number | null) {
    fellBack.value = false
    applyCurrent(id)
}

function applyCurrent(id: number | null) {
    currentProjectId.value = id
    if (id === null) {
        window.localStorage.removeItem(PROJECT_STORAGE_KEY)
        return
    }
    window.localStorage.setItem(PROJECT_STORAGE_KEY, String(id))
}

/** 建项目(仅管理员; 服务端也会拦)。建完自动切过去 —— 建完还留在旧项目会很莫名其妙。 */
async function create(name: string, description = ''): Promise<Project> {
    const project = await createProject({ name, description })
    projects.value = await listProjects()
    selectProject(project.id)
    return project
}

/** 仅供测试: 重置模块级状态。 */
export function __resetProjectForTest() {
    projects.value = []
    currentProjectId.value = null
    fellBack.value = false
    errorText.value = ''
}
