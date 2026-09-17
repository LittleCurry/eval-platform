// 会话状态(M7-1)。
//
// 为什么不用 pinia: 这个项目只有"当前用户"这一份全局状态, 引一个状态库是杀鸡用牛刀。
// 一个模块级的 reactive + localStorage 就够, 依赖也更少。
//
// 为什么 token 放 localStorage 而不是 httpOnly cookie: 前后端分离 + 无状态 JWT,
// token 必须由 JS 取出来塞进 Authorization 头。代价是 XSS 能偷 token ——
// 所以前端不开 v-html 渲染用户内容、后端响应里绝不带口令哈希。这个取舍写进 D22。

import { computed, reactive, readonly } from 'vue'
import * as authApi from '../api/auth'
import { setTokenProvider, setUnauthorizedHandler } from '../api/client'
import { parseSession, isUsable, type Session } from '../utils/session'
import type { UserAccount } from '../api/types'

const STORAGE_KEY = 'eval.session'

interface SessionState {
    session: Session | null
    /** 会话状态是否已经确认过(/auth/me 或 localStorage) —— 路由守卫要等它, 否则会闪一下登录页。 */
    ready: boolean
}

const state = reactive<SessionState>({ session: null, ready: false })

/** 从 localStorage 恢复(同步, 不校验 token 是否仍被服务端接受)。 */
function loadFromStorage() {
    state.session = parseSession(window.localStorage.getItem(STORAGE_KEY))
    if (state.session && !isUsable(state.session)) {
        // 过期就地清掉: 留着一个过期 token 只会让每个请求都先 401 再跳登录
        clear()
    }
}

function persist(session: Session | null) {
    state.session = session
    if (session) {
        window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session))
    } else {
        window.localStorage.removeItem(STORAGE_KEY)
    }
}

function clear() {
    persist(null)
}

// client 每次请求都问这里要 token —— 单向依赖, 不会成环
setTokenProvider(() => state.session?.token ?? null)

/** 收到 401: 清会话并跳登录页(带 redirect, 登录后回原处)。 */
setUnauthorizedHandler(() => {
    if (!state.session) return
    const current = window.location.pathname + window.location.search
    clear()
    const target = current.startsWith('/login') ? '/login' : `/login?redirect=${encodeURIComponent(current)}`
    // 用 location 而不是 router: client 层不该依赖 router 实例(初始化顺序不确定)
    window.location.assign(target)
})

export function useSession() {
    return {
        user: computed(() => state.session?.user ?? null),
        token: computed(() => state.session?.token ?? null),
        role: computed(() => state.session?.user.role ?? null),
        loggedIn: computed(() => isUsable(state.session)),
        ready: computed(() => state.ready),
        session: readonly(state),
    }
}

/** 应用启动时调用一次: 先用本地会话点亮界面, 再回服务端确认。 */
export async function initSession() {
    loadFromStorage()
    if (!state.session) {
        state.ready = true
        return
    }
    try {
        // 回服务端确认: token 可能已被停用/账号被删/角色被改。
        // 角色的**当前值**以服务端为准(token 里的是签发时的旧值)。
        const result = await authApi.getMe()
        persist({
            token: state.session.token,
            expiresAt: state.session.expiresAt,
            user: {
                id: result.user.id,
                email: result.user.email,
                name: result.user.name,
                role: result.user.role,
            },
        })
    } catch {
        // 确认失败: 401 已由 client 的钩子处理; 其它错误(网络/后端没起来)也不该让前端卡死,
        // 保留本地会话让用户看到界面, 后续请求会再暴露问题。
        if (state.session && !isUsable(state.session)) clear()
    } finally {
        state.ready = true
    }
}

export async function signIn(email: string, password: string) {
    const result = await authApi.login(email, password)
    persist({ token: result.token, expiresAt: result.expires_at, user: toSessionUser(result.user) })
    state.ready = true
    return result
}

export async function signUpFirstAdmin(email: string, name: string, password: string) {
    const result = await authApi.register(email, name, password)
    persist({ token: result.token, expiresAt: result.expires_at, user: toSessionUser(result.user) })
    state.ready = true
    return result
}

export async function signOut() {
    try {
        await authApi.logout()
    } catch {
        // 服务端只是回一句"请丢弃 token", 失败也不影响本地退出
    }
    clear()
}

/** 管理员改了自己的信息/角色后, 用它把本地会话同步过来。 */
export function syncUser(user: UserAccount) {
    if (!state.session) return
    persist({ ...state.session, user: toSessionUser(user) })
}

function toSessionUser(user: UserAccount) {
    return { id: user.id, email: user.email, name: user.name, role: user.role }
}

/** 仅供测试: 重置模块级状态。 */
export function __resetSessionForTest() {
    clear()
    state.ready = false
}
