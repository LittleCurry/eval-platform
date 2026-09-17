import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './routes'
import { initSession, useSession } from '../composables/useSession'
import { guardDecision, safeRedirect } from '../utils/session'

const router = createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes,
})

/**
 * 全局路由守卫(M7-1)。
 *
 * 三件事:
 *  1. **启动时先把会话确认过**(initSession)再去判断 —— 否则刷新页面会先闪一下登录页;
 *  2. 未登录 -> /login?redirect=原地址(登录后回原处, 只接受站内路径);
 *  3. 角色不足 -> 留在原地提示, **不跳登录页**(跳了会让人以为"没登录", 反复登录还是进不去)。
 */
router.beforeEach(async (to) => {
    const session = useSession()
    if (!session.ready.value) {
        await initSession()
    }

    const decision = guardDecision({
        requiresAuth: !to.meta.public,
        minRole: typeof to.meta.minRole === 'string' ? to.meta.minRole : undefined,
        role: session.role.value,
        loggedIn: session.loggedIn.value,
    })

    if (decision.kind === 'login') {
        return { path: '/login', query: { redirect: safeRedirect(to.fullPath) } }
    }
    if (decision.kind === 'forbidden') {
        // 用一个轻量提示页而不是 alert: 刷新/回退后信息还在, 也能看到自己当前的角色
        return { path: '/forbidden', query: { from: to.fullPath, need: String(to.meta.minRole ?? '') } }
    }
    return true
})

router.afterEach((to) => {
    document.title = `${String(to.meta.title ?? 'Eval Platform')} · Eval Platform`
})

export default router