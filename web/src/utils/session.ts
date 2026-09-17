// 会话与权限的纯逻辑(M7-1)。纯函数, 便于单测。
//
// 前端权限的作用是**不让人白点**: 该藏的按钮藏起来、该拦的路由拦下来。
// 真正的安全边界永远在服务端 —— 这里的每个判断, 后端都有一份独立实现
// (auth/rbac.go)。两边口径必须一致, 所以角色/动作的名字直接照抄后端。

import type { TagType } from './format'

/** 与后端 store.Role* 一致。 */
export const ROLE_ADMIN = 'admin'
export const ROLE_EDITOR = 'editor'
export const ROLE_VIEWER = 'viewer'

export const ROLE_LABELS: Record<string, string> = {
    admin: '管理员',
    editor: '编辑者',
    viewer: '只读',
}

/** 与后端 auth.Action* 一致。 */
export type Action = 'read' | 'write' | 'submit' | 'delete' | 'admin'

const ROLE_ACTIONS: Record<string, Action[]> = {
    viewer: ['read'],
    editor: ['read', 'write', 'submit'],
    admin: ['read', 'write', 'submit', 'delete', 'admin'],
}

export function roleLabel(role?: string | null): string {
    if (!role) return '未登录'
    return ROLE_LABELS[role] ?? role
}

export function roleTagType(role?: string | null): TagType {
    switch (role) {
        case ROLE_ADMIN:
            return 'error' // 管理员是危险角色, 用醒目的颜色
        case ROLE_EDITOR:
            return 'info'
        case ROLE_VIEWER:
            return 'default'
        default:
            return 'default'
    }
}

/** 当前角色能否执行某动作(与后端权限矩阵同一张表)。 */
export function can(role: string | null | undefined, action: Action): boolean {
    if (!role) return false
    return (ROLE_ACTIONS[role] ?? []).includes(action)
}

export function validRole(role: string): boolean {
    return role in ROLE_ACTIONS
}

/** 会话快照(存在 localStorage 里的东西)。 */
export interface SessionUser {
    id: number
    email: string
    name: string
    role: string
}

export interface Session {
    token: string
    expiresAt: string
    user: SessionUser
}

/** token 是否已过期(留 60 秒余量: 别让请求正好卡在过期瞬间)。 */
export function isExpired(expiresAt: string | undefined | null, now = Date.now()): boolean {
    if (!expiresAt) return true
    const at = Date.parse(expiresAt)
    if (Number.isNaN(at)) return true
    return at - 60_000 <= now
}

/** 会话是否可用: token 存在 + 未过期 + 有用户信息。 */
export function isUsable(session: Session | null | undefined, now = Date.now()): boolean {
    if (!session || !session.token || !session.user) return false
    return !isExpired(session.expiresAt, now)
}

/** 从任意 JSON(可能是旧版本/被手改过的 localStorage)里安全地读会话。 */
export function parseSession(raw: string | null | undefined): Session | null {
    if (!raw) return null
    try {
        const parsed = JSON.parse(raw) as Partial<Session>
        if (!parsed || typeof parsed.token !== 'string' || !parsed.user) return null
        const user = parsed.user as Partial<SessionUser>
        if (typeof user.id !== 'number' || typeof user.email !== 'string' || typeof user.role !== 'string') {
            return null
        }
        return {
            token: parsed.token,
            expiresAt: typeof parsed.expiresAt === 'string' ? parsed.expiresAt : '',
            user: { id: user.id, email: user.email, name: user.name ?? '', role: user.role },
        }
    } catch {
        // localStorage 里可能是任何东西(手改/旧版本): 解析失败当未登录, 不要抛
        return null
    }
}

/** 路由守卫的判断结果。 */
export type GuardDecision =
    | { kind: 'allow' }
    | { kind: 'login'; reason: string }
    | { kind: 'forbidden'; reason: string }

/**
 * 路由守卫决策。
 *
 * 三种结果对应三种体验:
 *   allow     —— 直接进;
 *   login     —— 没登录/过期: 跳登录页并带上 redirect(登录后回到原处);
 *   forbidden —— 登录了但角色不够: **不要跳登录页**, 否则用户会以为自己没登录,
 *                反复登录还是进不去。留在原地给一句"需要管理员权限"更清楚。
 */
export function guardDecision(options: {
    requiresAuth: boolean
    minRole?: string
    role?: string | null
    loggedIn: boolean
}): GuardDecision {
    if (!options.requiresAuth) return { kind: 'allow' }
    if (!options.loggedIn) {
        return { kind: 'login', reason: '请先登录' }
    }
    if (options.minRole && !atLeast(options.role, options.minRole)) {
        return { kind: 'forbidden', reason: `需要${roleLabel(options.minRole)}权限` }
    }
    return { kind: 'allow' }
}

const ROLE_RANK: Record<string, number> = { viewer: 1, editor: 2, admin: 3 }

export function atLeast(role: string | null | undefined, min: string): boolean {
    const rank = role ? ROLE_RANK[role] ?? 0 : 0
    const minRank = ROLE_RANK[min] ?? 99
    return rank >= minRank
}

/** 菜单项的最小角色要求。 */
export interface MenuPermission {
    key: string
    minRole?: string
}

/**
 * 按角色裁剪菜单。
 *
 * 只读账号看到"用户管理"点进去必然被拦, 那是纯浪费 —— 直接不显示。
 * 但**业务页面全部保留**(viewer 也能看报告/对比/标注结果): 藏掉只读用户唯一能用的页面
 * 才是真的错。
 */
export function visibleMenuKeys(role: string | null | undefined, permissions: MenuPermission[]): string[] {
    return permissions
        .filter((item) => !item.minRole || atLeast(role, item.minRole))
        .map((item) => item.key)
}

/** 登录后回跳地址: 只接受站内绝对路径, 防止被拿来做开放重定向。 */
export function safeRedirect(target: unknown, fallback = '/'): string {
    if (typeof target !== 'string') return fallback
    if (!target.startsWith('/') || target.startsWith('//')) return fallback
    return target
}
