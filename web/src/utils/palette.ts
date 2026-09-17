// 语义色板的**唯一出处**(M7-3 配色统一)。
//
// 问题: 同一个"红"以前散落在多处 —— naive-ui 的 `NTag type="error"` 自己查表是 #d03050,
// 而报告页的 CSS 左边框、断言计数文字、校准页的热力格各自硬编码了十六进制/ rgba。
// 改一处忘一处, 同一个页面就会出现两种红; 深色主题下还会更难看。
//
// 约定(两条, 配套测试 palette.spec.ts 会强制):
//   1. 交给组件的语义, 一律用 `type: 'error' | 'warning' | ...`(naive-ui 自己查表, 与下表同源);
//   2. 自绘的东西(CSS 边框/底色、内联文字色)只能用本文件导出的函数或 CSS 变量,
//      **src 下不允许再出现十六进制/rgb 字面量**(测试里有一条源码扫描兜着)。
//
// 表里的十六进制**不是拍脑袋定的**: 与 naive-ui light 主题的
// successColor/warningColor/errorColor/infoColor 对齐(node_modules/naive-ui/es/_styles/common/light.mjs),
// 这样"标签的绿"和"边框的绿"是同一个绿。

import type { TagType } from './format'

/**
 * 中性的"默认"语义没有对应的品牌色: naive-ui 的 default 标签用的是中性文字色,
 * 自绘时用一个中灰近似(浅色底上足够, 且不会喧宾夺主)。
 */
export const NEUTRAL_HEX = '#8a8a8a'

/** 语义 → 颜色。key 就是 naive-ui 的 TagType, 便于两边对照。 */
export const SEMANTIC_HEX: Record<TagType, string> = {
    default: NEUTRAL_HEX,
    success: '#18a058',
    warning: '#f0a020',
    error: '#d03050',
    info: '#2080f0',
}

/** 语义色, 缺失/未知一律回落到中性色(不要 undefined 传到 style 里变成透明的黑)。 */
export function tagHex(type?: TagType | string | null): string {
    if (!type) return NEUTRAL_HEX
    return SEMANTIC_HEX[type as TagType] ?? NEUTRAL_HEX
}

/**
 * `#18a058` + 0.08 → `rgba(24, 160, 88, 0.08)`。
 * 只接受 3/6 位十六进制: 传错就**当场报错**, 而不是悄悄渲染成透明(那种 bug 最难看出来)。
 */
export function tint(hex: string, alpha: number): string {
    const raw = hex.trim().replace(/^#/, '')
    const expanded = raw.length === 3 ? raw.split('').map((ch) => ch + ch).join('') : raw
    if (!/^[0-9a-fA-F]{6}$/.test(expanded)) {
        throw new Error(`tint: 不是合法的十六进制色值: ${hex}`)
    }
    if (!(alpha >= 0 && alpha <= 1)) {
        throw new Error(`tint: alpha 必须在 0~1 之间: ${alpha}`)
    }
    const r = parseInt(expanded.slice(0, 2), 16)
    const g = parseInt(expanded.slice(2, 4), 16)
    const b = parseInt(expanded.slice(4, 6), 16)
    return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

/** 语义色的浅底(用于"这一行有问题"这类需要整块着色但不是纯色填充的地方)。 */
export function softTint(type?: TagType | string | null, alpha = 0.1): string {
    return tint(tagHex(type), alpha)
}

/** 中性浅底: 代码块/答案块这类"弱提示"背景, 深浅主题下都能看(用灰而不是白/黑)。 */
export const NEUTRAL_SOFT = tint(NEUTRAL_HEX, 0.08)

/** 中性浅底(更重一档): 表格选中行、当前题高亮。 */
export const NEUTRAL_STRONG = tint(NEUTRAL_HEX, 0.12)

/** 中性弱文字(与 naive-ui 的 NText depth=3 观感接近)。 */
export const NEUTRAL_TEXT = tint(NEUTRAL_HEX, 0.9)

/**
 * 挂到 `<html>` 上的 CSS 变量(main.ts 里执行一次)。
 * 组件的 scoped CSS 里写 `border-left-color: var(--ev-error)` 即可,
 * 不必把色值抄进 SFC, 也不必为了有个类名而多写一层包装元素。
 */
export function cssVars(): Record<string, string> {
    const vars: Record<string, string> = {
        '--ev-neutral': NEUTRAL_HEX,
        '--ev-neutral-soft': NEUTRAL_SOFT,
        '--ev-neutral-strong': NEUTRAL_STRONG,
        '--ev-neutral-text': NEUTRAL_TEXT,
    }
    for (const [type, hex] of Object.entries(SEMANTIC_HEX)) {
        vars[`--ev-${type}`] = hex
        vars[`--ev-${type}-soft`] = tint(hex, 0.1)
    }
    return vars
}
