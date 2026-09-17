import { describe, expect, it } from 'vitest'
import {
    NEUTRAL_HEX,
    NEUTRAL_SOFT,
    NEUTRAL_STRONG,
    NEUTRAL_TEXT,
    SEMANTIC_HEX,
    cssVars,
    softTint,
    tagHex,
    tint,
} from './palette'

/** naive-ui light 主题的语义色(light.mjs) —— 钉在这里, 上游改色我们能立刻发现。 */
const NAIVE_LIGHT: Record<string, string> = {
    success: '#18a058',
    warning: '#f0a020',
    error: '#d03050',
    info: '#2080f0',
}

describe('SEMANTIC_HEX / tagHex', () => {
    it('语义色与 naive-ui light 主题一致(标签的绿 = 边框的绿)', () => {
        for (const [type, hex] of Object.entries(NAIVE_LIGHT)) {
            expect(SEMANTIC_HEX[type as keyof typeof SEMANTIC_HEX]).toBe(hex)
        }
    })

    it('五个语义都有色值, 且都是合法的六位十六进制', () => {
        expect(Object.keys(SEMANTIC_HEX).sort()).toEqual(['default', 'error', 'info', 'success', 'warning'])
        for (const hex of Object.values(SEMANTIC_HEX)) {
            expect(hex).toMatch(/^#[0-9a-f]{6}$/)
        }
    })

    it('缺失或未知语义回落到中性色(不产生 undefined)', () => {
        expect(tagHex(undefined)).toBe(NEUTRAL_HEX)
        expect(tagHex(null)).toBe(NEUTRAL_HEX)
        expect(tagHex('')).toBe(NEUTRAL_HEX)
        expect(tagHex('purple')).toBe(NEUTRAL_HEX)
        expect(tagHex('error')).toBe('#d03050')
    })
})

describe('tint / softTint', () => {
    it('十六进制转 rgba', () => {
        expect(tint('#18a058', 0.08)).toBe('rgba(24, 160, 88, 0.08)')
        expect(tint('#ffffff', 1)).toBe('rgba(255, 255, 255, 1)')
        expect(tint('2080f0', 0.5)).toBe('rgba(32, 128, 240, 0.5)')
    })

    it('支持三位简写', () => {
        expect(tint('#fff', 0.2)).toBe('rgba(255, 255, 255, 0.2)')
        expect(tint('#0f0', 0.5)).toBe('rgba(0, 255, 0, 0.5)')
    })

    it('非法输入当场报错(而不是静默渲染成透明)', () => {
        expect(() => tint('#12345', 0.5)).toThrow(/十六进制/)
        expect(() => tint('#gggggg', 0.5)).toThrow(/十六进制/)
        expect(() => tint('#123456', 1.5)).toThrow(/alpha/)
        expect(() => tint('#123456', -0.1)).toThrow(/alpha/)
    })

    it('softTint 走语义色, 未知名用中性色', () => {
        expect(softTint('error')).toBe('rgba(208, 48, 80, 0.1)')
        expect(softTint('error', 0.2)).toBe('rgba(208, 48, 80, 0.2)')
        expect(softTint(undefined)).toBe(tint(NEUTRAL_HEX, 0.1))
    })

    it('三个中性常量彼此不同且都不透明', () => {
        const all = [NEUTRAL_SOFT, NEUTRAL_STRONG, NEUTRAL_TEXT]
        expect(new Set(all).size).toBe(3)
        for (const value of all) expect(value).toMatch(/^rgba\(\d+, \d+, \d+, 0\.\d+\)$/)
    })
})

describe('cssVars', () => {
    it('每个语义都有实色与浅底两个变量', () => {
        const vars = cssVars()
        for (const type of Object.keys(SEMANTIC_HEX)) {
            expect(vars[`--ev-${type}`]).toBe(SEMANTIC_HEX[type as keyof typeof SEMANTIC_HEX])
            expect(vars[`--ev-${type}-soft`]).toMatch(/^rgba\(/)
        }
        expect(vars['--ev-neutral-soft']).toBe(NEUTRAL_SOFT)
        expect(vars['--ev-neutral-strong']).toBe(NEUTRAL_STRONG)
        expect(vars['--ev-neutral-text']).toBe(NEUTRAL_TEXT)
    })

    it('变量名统一 --ev- 前缀(避免和 naive-ui 的 --n- 变量撞车)', () => {
        for (const name of Object.keys(cssVars())) {
            expect(name.startsWith('--ev-')).toBe(true)
        }
    })
})

/**
 * 这条测试是"配色统一"的**执行力**所在: 光有 palette.ts 没人拦着, 下一个页面照样会
 * 抄一个 #d03050 进去。扫描范围是整个 src(排除 palette 自身与测试文件),
 * 命中就说明有人把色值写在了外面 —— 报错信息里直接给出改法。
 */
describe('色值不外泄', () => {
    const HEX = /#[0-9a-fA-F]{3}\b|#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{8}\b/
    const RGB = /\brgba?\(\s*\d+\s*,/

    /**
     * 源码用 Vite 的 `?raw` 导入读进来(而不是 node:fs): 前者不需要给 DOM 工程
     * 塞 node 类型, 而且拿到的就是"打包时会看到的那份源码"。
     */
    const SOURCES = import.meta.glob(['../**/*.ts', '../**/*.vue'], {
        query: '?raw',
        import: 'default',
        eager: true,
    }) as Record<string, string>

    /** 排除色板自身与测试文件(它们本来就在讨论颜色)。 */
    function checkedSources(): [string, string][] {
        return Object.entries(SOURCES).filter(
            ([path]) => !path.endsWith('.spec.ts') && !path.endsWith('/palette.ts'),
        )
    }

    it('扫到的源码文件数量合理(免得 glob 写错导致这条测试空转)', () => {
        const files = checkedSources()
        expect(files.length).toBeGreaterThan(10)
        expect(files.some(([path]) => path.endsWith('views/RunReportView.vue'))).toBe(true)
    })

    it('除 palette.ts 外, src 下不出现十六进制或 rgb 色值', () => {
        const offenders: string[] = []
        for (const [path, content] of checkedSources()) {
            content.split('\n').forEach((line, index) => {
                if (HEX.test(line) || RGB.test(line)) {
                    offenders.push(`${path.replace('../', 'src/')}:${index + 1} ${line.trim()}`)
                }
            })
        }
        expect(
            offenders,
            '色值必须集中到 utils/palette.ts: 用 tagHex()/softTint()/tint() 或 CSS 变量 var(--ev-error) 等',
        ).toEqual([])
    })
})
