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
    it('包含 home / login / 三个业务页', () => {
        const names = allNames()
        expect(names).toContain('home')
        expect(names).toContain('login')
        expect(names).toContain('corpora')
        expect(names).toContain('datasets')
        expect(names).toContain('dataset-detail')
    })

    it('业务页路由路径正确', () => {
        const paths = allPaths()
        expect(paths).toContain('/corpora')
        expect(paths).toContain('/datasets')
        expect(paths).toContain('/datasets/:id')
    })

    it('根路径挂载布局并默认渲染首页', () => {
        const root = routes.find((r) => r.path === '/')
        expect(root?.children?.some((c) => c.path === '' && c.name === 'home')).toBe(true)
    })
})