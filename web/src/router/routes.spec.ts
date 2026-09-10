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

function allPaths(): string[] {
    const paths: string[] = []
    for (const r of routes) {
        if (r.children) {
            for (const c of r.children) paths.push((r.path === '/' ? '/' : r.path + '/') + c.path)
        } else {
            paths.push(r.path)
        }
    }
    return paths
}

describe('路由表', () => {
    it('包含 home / login / 各业务页', () => {
        const names = allNames()
        for (const expected of ['home', 'login', 'corpora', 'datasets', 'dataset-detail', 'runs', 'run-detail']) {
            expect(names).toContain(expected)
        }
    })

    it('业务页路由路径正确', () => {
        const paths = allPaths()
        for (const expected of ['/corpora', '/datasets', '/datasets/:id', '/runs', '/runs/:id']) {
            expect(paths).toContain(expected)
        }
    })

    it('根路径挂载布局并默认渲染首页', () => {
        const root = routes.find((r) => r.path === '/')
        expect(root?.children?.some((c) => c.path === '' && c.name === 'home')).toBe(true)
    })
})