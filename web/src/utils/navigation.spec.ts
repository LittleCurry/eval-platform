import { describe, expect, it } from 'vitest'
import { routes } from '../router/routes'
import { NAV_TREE, activeNavKey, navKeys, navLabel, visibleNav } from './navigation'

/** routes.ts 里所有子路由的名字(布局路由的 children)。 */
function routeNames(): string[] {
    const names: string[] = []
    for (const item of routes) {
        for (const child of item.children ?? []) {
            if (typeof child.name === 'string') names.push(child.name)
        }
    }
    return names
}

/** 这些页面**不该**出现在菜单里: 详情页从列表点进去, 登录/无权限页不该被主动点。 */
const NOT_IN_MENU = ['dataset-detail', 'run-detail', 'login', 'forbidden']

describe('导航与路由的双向一致性', () => {
    it('该进菜单的页面一个都不能漏(新增页面忘了加菜单会被这条抓住)', () => {
        const menu = navKeys()
        const missing = routeNames().filter((name) => !NOT_IN_MENU.includes(name) && !menu.includes(name))
        expect(missing).toEqual([])
    })

    it('菜单里的每一项都必须有对应路由(改路由名忘了改菜单会被抓住)', () => {
        const names = routeNames()
        const dangling = navKeys().filter((key) => !names.includes(key))
        expect(dangling).toEqual([])
    })

    it('菜单 key 不重复(重复会导致点了跳错页)', () => {
        const keys = navKeys()
        expect(new Set(keys).size).toBe(keys.length)
    })

    it('每个菜单项都有非空中文标签', () => {
        for (const item of NAV_TREE) {
            expect(item.label.trim()).not.toBe('')
            expect(item.label).not.toBe(item.key)
        }
    })

    it('详情页不进菜单, 但会高亮它的父列表页', () => {
        expect(navKeys()).not.toContain('run-detail')
        expect(activeNavKey('run-detail')).toBe('runs')
        expect(activeNavKey('dataset-detail')).toBe('datasets')
        expect(activeNavKey('gold')).toBe('gold')
        expect(activeNavKey(undefined)).toBe('')
    })

    it('标签查找: 认识的 key 给中文, 不认识的回落成 key(便于暴露漏加菜单)', () => {
        expect(navLabel('gold')).toBe('金标打分')
        expect(navLabel('brand-new-page')).toBe('brand-new-page')
    })
})

describe('按角色过滤', () => {
    it('管理员看得到全部, 包含用户管理', () => {
        expect(navKeys(visibleNav('admin'))).toContain('users')
        expect(visibleNav('admin').length).toBe(NAV_TREE.length)
    })

    it('编辑者/只读看不到用户管理, 但业务页面一个不少', () => {
        for (const role of ['editor', 'viewer']) {
            const keys = navKeys(visibleNav(role))
            expect(keys).not.toContain('users')
            // 只读账号最需要的是"看报告/看对比/看标注结果", 这些必须还在
            for (const key of ['home', 'runs', 'compare', 'annotations', 'calibration']) {
                expect(keys).toContain(key)
            }
        }
    })

    it('未登录时同样只隐藏有门槛的项(避免闪出无权入口)', () => {
        expect(navKeys(visibleNav(null))).not.toContain('users')
        expect(navKeys(visibleNav('ghost'))).not.toContain('users')
    })
})

describe('导航规模(顶栏放得下)', () => {
    it('平铺项数不超过 12 —— 再多就该考虑分组, 而不是继续挤', () => {
        // 这条不是形式主义: M6 之后菜单从 8 项涨到 11 项, 顶栏就是在这里开始截断的。
        // 触发时应该重新设计信息架构(分组/二级菜单), 而不是再砍间距。
        expect(NAV_TREE.length).toBeLessThanOrEqual(12)
    })

    it('最宽标签不超过 12 个"半角宽度"(过长会把整行顶爆)', () => {
        // 按显示宽度算而不是字符数: 汉字占 2 个半角宽度, "Judge 校准"=10、 "标注工作台"=10。
        // 12 相当于"最多 6 个汉字", 是顶栏一行放得下的上限。
        const displayWidth = (label: string) =>
            [...label].reduce((sum, char) => sum + (/[\u4e00-\u9fa5]/.test(char) ? 2 : 1), 0)
        for (const item of NAV_TREE) {
            expect(displayWidth(item.label)).toBeLessThanOrEqual(12)
        }
    })
})
