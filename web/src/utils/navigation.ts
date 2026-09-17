// 导航模型(M7-3)。纯数据 + 纯函数, 便于单测。
//
// 为什么把菜单从布局组件里抽出来: 菜单是"页面清单"的第二个副本(第一个是 routes.ts),
// 而**漏一个页面的后果是静默的** —— 页面还在、路由还能手输访问, 只是菜单里点不到,
// 没人会发现。抽成纯数据后可以写一条双向断言(见 navigation.spec.ts):
// 菜单里的 key 必须有对应路由, 该进菜单的路由也必须都在菜单里。

import { atLeast } from './session'

/** 一个菜单项(不含任何 naive-ui 类型, 保持可测)。 */
export interface NavNode {
    /** 与 router 的 name 一致 —— 菜单项点击就是 router.push({ name: key })。 */
    key: string
    label: string
    /** 低于该角色则整个隐藏(只给"点进去必然被拦"的页面设门槛)。 */
    minRole?: string
}

/**
 * 菜单清单。**顺序即展示顺序**, 按"日常动线"排:
 * 看概览 → 管数据(语料/数据集) → 跑实验看报告 → 标注与校准 → 配置与对比 → 管人。
 *
 * 全部平铺、不做二级菜单: 这些页面都是日常入口, 藏进子菜单只会多一次点击;
 * 顶栏挤不下应该靠"导航独占一行 + 收紧间距"解决(见 DefaultLayout), 而不是让人多点。
 */
export const NAV_TREE: NavNode[] = [
    { key: 'home', label: '概览' },
    { key: 'corpora', label: '语料库' },
    { key: 'datasets', label: '数据集' },
    { key: 'runs', label: '运行报告' },
    { key: 'annotations', label: '标注工作台' },
    { key: 'gold', label: '金标打分' },
    { key: 'calibration', label: 'Judge 校准' },
    { key: 'closure', label: '标注闭环' },
    { key: 'profiles', label: '配置模板' },
    { key: 'compare', label: 'A/B 对比' },
    { key: 'users', label: '用户管理', minRole: 'admin' },
]

/**
 * 按角色过滤菜单。
 *
 * 只过滤"点进去必然被 403"的项(用户管理); 业务页面全部保留 ——
 * 只读账号也要能看报告/对比/标注结果, 那才是他来这个系统要做的事。
 */
export function visibleNav(role: string | null | undefined, tree: NavNode[] = NAV_TREE): NavNode[] {
    return tree.filter((item) => !item.minRole || atLeast(role, item.minRole))
}

/** 菜单里所有 key(顺序与展示顺序一致)。 */
export function navKeys(tree: NavNode[] = NAV_TREE): string[] {
    return tree.map((item) => item.key)
}

/** key -> 中文标签; 找不到时回落成 key 本身(便于发现"忘了加菜单")。 */
export function navLabel(key: string, tree: NavNode[] = NAV_TREE): string {
    return tree.find((item) => item.key === key)?.label ?? key
}

/**
 * 当前路由应高亮哪个菜单项。
 *
 * 详情页不是菜单项, 但它是从某个列表页点进去的 —— 高亮父列表页才符合人的预期
 * (在 run 详情里看到"运行报告"是选中的, 而不是"什么都没选中")。
 */
export function activeNavKey(routeName: string | null | undefined): string {
    const name = String(routeName ?? '')
    if (name === 'dataset-detail') return 'datasets'
    if (name === 'run-detail') return 'runs'
    return name
}
