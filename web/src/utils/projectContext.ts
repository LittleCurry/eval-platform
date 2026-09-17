// 项目上下文的纯逻辑(M7-2)。纯函数, 便于单测。
//
// 背景(D24): 不建成员表, **全员可见** —— 所以"项目"在这里解决的是另外两件事:
//  1. 数据不串台: 语料/数据集/run 都属于某个项目, 页面看到的是**当前项目**的东西;
//  2. 归属可见: 谁建的写在 created_by / owner_email 上。
//
// 这个模块只做"当前该用哪个项目"的判断, 不发请求(请求在 composables/useProject.ts)。

export const PROJECT_STORAGE_KEY = 'eval.currentProjectId'

/** 从 localStorage 里读出的字符串还原成项目 id; 非法一律 null(不猜)。 */
export function parseStoredProjectId(raw: string | null | undefined): number | null {
    if (!raw) return null
    const value = Number(raw)
    if (!Number.isFinite(value) || !Number.isInteger(value) || value <= 0) return null
    return value
}

/**
 * 决定当前项目。
 *
 * 规则(顺序即优先级):
 *  1. 记住的 id 仍然存在 -> 用它(用户上次在看哪个项目, 就该接着看哪个);
 *  2. 记的 id 没了(项目被删/换了环境) -> 回退到第一个项目, 并**让调用方知道发生了回退**
 *     (fallback=true) —— 静默换项目最坑: 人会以为自己在看 A, 其实在看 B;
 *  3. 一个项目都没有 -> null(页面要显示"先建一个项目"的引导, 而不是空白表格)。
 */
export function resolveCurrentProject(
    projects: { id: number }[],
    storedId: number | null,
): { projectId: number | null; fallback: boolean } {
    if (projects.length === 0) return { projectId: null, fallback: false }
    if (storedId !== null && projects.some((item) => item.id === storedId)) {
        return { projectId: storedId, fallback: false }
    }
    return { projectId: projects[0].id, fallback: storedId !== null }
}

/** 项目下拉里的文案: "#1 · 知简CRM评测"。 */
export function projectLabel(project: { id: number; name: string }): string {
    return `#${project.id} · ${project.name}`
}

/**
 * 项目归属的一句话: "由 curry@example.com 创建" / "创建者未知(早期数据)"。
 *
 * 为什么要专门处理"未知": projects 在 M1 就有了, 那批数据的 created_by 是 NULL ——
 * 显示成空白会让人以为是加载失败。
 */
export function ownerText(project: { owner_email?: string; created_by?: number }): string {
    if (project.owner_email) return `由 ${project.owner_email} 创建`
    if (typeof project.created_by === 'number') return `创建者 #${project.created_by}`
    return '创建者未知（M7 之前建立的项目）'
}

/** 没有项目时给一句能照做的事, 而不是让用户对着空表格发呆。 */
export function emptyProjectHint(canCreate: boolean): string {
    if (canCreate) {
        return '还没有任何项目：先建一个项目，语料库、数据集和实验都挂在它下面'
    }
    return '还没有任何项目：请让管理员先建一个项目'
}

/** 切换项目后需要提示的话(告诉用户"你看的东西换了", 而不是让他自己发现)。 */
export function switchNotice(from: string, to: string): string {
    return `已切换到 ${to}（原 ${from}）—— 列表与报告都会跟着换，注意别看串了`
}
