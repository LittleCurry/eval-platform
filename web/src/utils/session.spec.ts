import { describe, expect, it } from 'vitest'
import {
    ROLE_ADMIN,
    ROLE_EDITOR,
    ROLE_VIEWER,
    atLeast,
    can,
    guardDecision,
    isExpired,
    isUsable,
    parseSession,
    roleLabel,
    roleTagType,
    safeRedirect,
    validRole,
    visibleMenuKeys,
    type Session,
} from './session'

function session(overrides: Partial<Session> = {}): Session {
    return {
        token: 'tok',
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        user: { id: 1, email: 'a@b.com', name: '甲', role: ROLE_EDITOR },
        ...overrides,
    }
}

describe('权限矩阵(与后端同一张表)', () => {
    it('viewer 只能读', () => {
        expect(can(ROLE_VIEWER, 'read')).toBe(true)
        for (const action of ['write', 'submit', 'delete', 'admin'] as const) {
            expect(can(ROLE_VIEWER, action)).toBe(false)
        }
    })

    it('editor 能写能跑实验, 不能删数据/管人', () => {
        expect(can(ROLE_EDITOR, 'read')).toBe(true)
        expect(can(ROLE_EDITOR, 'write')).toBe(true)
        expect(can(ROLE_EDITOR, 'submit')).toBe(true)
        expect(can(ROLE_EDITOR, 'delete')).toBe(false)
        expect(can(ROLE_EDITOR, 'admin')).toBe(false)
    })

    it('admin 全都能', () => {
        for (const action of ['read', 'write', 'submit', 'delete', 'admin'] as const) {
            expect(can(ROLE_ADMIN, action)).toBe(true)
        }
    })

    it('未登录/未知角色一律拒绝(默认拒绝, 不是默认放行)', () => {
        expect(can(null, 'read')).toBe(false)
        expect(can(undefined, 'read')).toBe(false)
        expect(can('root', 'read')).toBe(false)
        expect(can('Admin', 'admin')).toBe(false)
    })

    it('角色文案与配色', () => {
        expect(roleLabel(ROLE_ADMIN)).toBe('管理员')
        expect(roleLabel(ROLE_VIEWER)).toBe('只读')
        expect(roleLabel(null)).toBe('未登录')
        expect(roleTagType(ROLE_ADMIN)).toBe('error')
        expect(roleTagType(ROLE_VIEWER)).toBe('default')
        expect(validRole('editor')).toBe(true)
        expect(validRole('superuser')).toBe(false)
    })
})

describe('isExpired / isUsable', () => {
    it('过期时间是过去就失效, 留 60 秒余量', () => {
        const now = Date.parse('2026-09-17T12:00:00Z')
        expect(isExpired('2026-09-17T13:00:00Z', now)).toBe(false)
        expect(isExpired('2026-09-17T11:00:00Z', now)).toBe(true)
        // 还剩 30 秒 -> 已算过期(别让请求正好卡在过期瞬间)
        expect(isExpired('2026-09-17T12:00:30Z', now)).toBe(true)
        expect(isExpired(undefined, now)).toBe(true)
        expect(isExpired('not-a-date', now)).toBe(true)
    })

    it('会话可用 = token + 用户 + 未过期', () => {
        expect(isUsable(session())).toBe(true)
        expect(isUsable(null)).toBe(false)
        expect(isUsable(session({ token: '' }))).toBe(false)
        expect(isUsable(session({ expiresAt: new Date(Date.now() - 1000).toISOString() }))).toBe(false)
    })
})

describe('parseSession', () => {
    it('正常会话能读出来', () => {
        const parsed = parseSession(JSON.stringify(session()))
        expect(parsed?.user.email).toBe('a@b.com')
        expect(parsed?.user.role).toBe(ROLE_EDITOR)
    })

    it('脏数据当未登录, 不抛异常(localStorage 里可能是任何东西)', () => {
        expect(parseSession(null)).toBeNull()
        expect(parseSession('')).toBeNull()
        expect(parseSession('not json')).toBeNull()
        expect(parseSession('{}')).toBeNull()
        expect(parseSession(JSON.stringify({ token: 't' }))).toBeNull()
        expect(parseSession(JSON.stringify({ token: 't', user: { email: 'a@b.com' } }))).toBeNull()
    })

    it('缺 name 时补空串(不让 undefined 进模板)', () => {
        const parsed = parseSession(JSON.stringify({
            token: 't', expiresAt: 'x', user: { id: 3, email: 'c@d.com', role: 'viewer' },
        }))
        expect(parsed?.user.name).toBe('')
    })
})

describe('guardDecision', () => {
    it('公开路由直接放行', () => {
        expect(guardDecision({ requiresAuth: false, loggedIn: false }).kind).toBe('allow')
    })

    it('未登录 -> 去登录页(带上原因)', () => {
        const decision = guardDecision({ requiresAuth: true, loggedIn: false })
        if (decision.kind !== 'login') throw new Error(`应为 login, 实际 ${decision.kind}`)
        expect(decision.reason).toBe('请先登录')
    })

    it('角色不够 -> 403 而不是再跳登录页', () => {
        // 关键体验差别: 跳登录页会让用户"反复登录还是进不去", 不知道该找谁开权限
        const decision = guardDecision({
            requiresAuth: true, loggedIn: true, role: ROLE_VIEWER, minRole: ROLE_ADMIN,
        })
        if (decision.kind !== 'forbidden') throw new Error(`应为 forbidden, 实际 ${decision.kind}`)
        expect(decision.reason).toContain('管理员')
    })

    it('角色够就放行', () => {
        expect(guardDecision({
            requiresAuth: true, loggedIn: true, role: ROLE_ADMIN, minRole: ROLE_ADMIN,
        }).kind).toBe('allow')
        expect(guardDecision({
            requiresAuth: true, loggedIn: true, role: ROLE_EDITOR, minRole: ROLE_EDITOR,
        }).kind).toBe('allow')
    })
})

describe('visibleMenuKeys', () => {
    const menu = [
        { key: 'home' },
        { key: 'runs' },
        { key: 'users', minRole: ROLE_ADMIN },
        { key: 'profiles', minRole: ROLE_EDITOR },
    ]

    it('只读账号看不到管人与需要写权限的入口, 但业务页面全在', () => {
        expect(visibleMenuKeys(ROLE_VIEWER, menu)).toEqual(['home', 'runs'])
    })

    it('编辑者能看到配置模板, 看不到用户管理', () => {
        expect(visibleMenuKeys(ROLE_EDITOR, menu)).toEqual(['home', 'runs', 'profiles'])
    })

    it('管理员全都看得到', () => {
        expect(visibleMenuKeys(ROLE_ADMIN, menu)).toEqual(['home', 'runs', 'users', 'profiles'])
    })

    it('未登录只看到无门槛项(避免闪烁出无权入口)', () => {
        expect(visibleMenuKeys(null, menu)).toEqual(['home', 'runs'])
    })
})

describe('safeRedirect', () => {
    it('只接受站内绝对路径: 防开放重定向', () => {
        expect(safeRedirect('/runs/155')).toBe('/runs/155')
        expect(safeRedirect('/closure?baseline=112')).toBe('/closure?baseline=112')
        expect(safeRedirect('//evil.com')).toBe('/')
        expect(safeRedirect('https://evil.com')).toBe('/')
        expect(safeRedirect('runs/155')).toBe('/')
        expect(safeRedirect(undefined)).toBe('/')
        expect(safeRedirect(123)).toBe('/')
    })

    it('可指定兜底页面', () => {
        expect(safeRedirect('https://evil.com', '/runs')).toBe('/runs')
    })
})

describe('atLeast', () => {
    it('按能力档比较', () => {
        expect(atLeast(ROLE_ADMIN, ROLE_EDITOR)).toBe(true)
        expect(atLeast(ROLE_EDITOR, ROLE_EDITOR)).toBe(true)
        expect(atLeast(ROLE_VIEWER, ROLE_EDITOR)).toBe(false)
        expect(atLeast(null, ROLE_VIEWER)).toBe(false)
        expect(atLeast('ghost', ROLE_VIEWER)).toBe(false)
    })
})
