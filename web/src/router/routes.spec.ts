import { describe, expect, it } from 'vitest'
import { routes } from './routes'

function allNames(): (string | symbol | undefined)[] {
    const names: (string | symbol | undefined)[] = []
    for (const r of routes) {
        names.push(r.name)
        if (r.children) for (const c of r.children) names.push(c.name)
    }
    return names
}

describe('路由表', () => {
    it('包含 home 与 login 两个命名路由', () => {
        const names = allNames()
        expect(names).toContain('home')
        expect(names).toContain('login')
    })

    it('根路径挂载布局并默认渲染首页', () => {
        const root = routes.find((r) => r.path === '/')
        expect(root?.children?.some((c) => c.path === '' && c.name === 'home')).toBe(true)
    })
})