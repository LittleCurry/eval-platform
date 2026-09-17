import { describe, expect, it } from 'vitest'
import { NText, NTooltip, type DataTableBaseColumn, type DataTableColumns } from 'naive-ui'
import type { RunCaseResult } from '../api/types'
import { tagHex } from './palette'
import {
    CASE_TABLE_SCROLL_X,
    CASE_TABLE_SPECS,
    FLEX_MIN_WIDTH,
    WORST_TABLE_SCROLL_X,
    WORST_TABLE_SPECS,
    buildColumns,
    caseTableColumns,
    claimsCell,
    fixedWidthTotal,
    scrollX,
    worstTableColumns,
    type ColumnSpec,
} from './reportTable'

function caseRow(overrides: Partial<RunCaseResult> = {}): RunCaseResult {
    return {
        case_id: 1,
        qid: 'Q001',
        question: '如何导出报表？',
        retrieved: [],
        metrics: {},
        flags: [],
        ...overrides,
    }
}

function columnOf(cols: DataTableColumns<RunCaseResult>, key: string): DataTableBaseColumn<RunCaseResult> {
    // 选择列/展开列没有 key, 这里只关心带 key 的普通列
    const found = (cols as DataTableBaseColumn<RunCaseResult>[]).find((item) => String(item.key) === key)
    expect(found, `表里缺少 ${key} 列`).toBeTruthy()
    return found as DataTableBaseColumn<RunCaseResult>
}

function renderText(cols: DataTableColumns<RunCaseResult>, key: string, row: RunCaseResult): string {
    return String(columnOf(cols, key).render?.(row, 0) ?? '')
}

const ALL_SPECS: [string, ColumnSpec[]][] = [
    ['全量单题结果表', CASE_TABLE_SPECS],
    ['最差用例表', WORST_TABLE_SPECS],
]

describe('列规格', () => {
    it.each(ALL_SPECS)('%s: 列 key 唯一', (_name, specs) => {
        const keys = specs.map((spec) => spec.key)
        expect(new Set(keys).size).toBe(keys.length)
    })

    it.each(ALL_SPECS)('%s: 只允许一个自适应列', (_name, specs) => {
        expect(specs.filter((spec) => spec.width === undefined).length).toBe(1)
    })

    it.each(ALL_SPECS)('%s: 每列都有下限宽度, 且当前宽度不低于下限', (_name, specs) => {
        for (const spec of specs) {
            expect(spec.minWidth, `${spec.key} 缺 minWidth`).toBeGreaterThan(0)
            if (spec.width !== undefined) {
                expect(spec.width, `${spec.key} 宽度小于下限`).toBeGreaterThanOrEqual(spec.minWidth as number)
            }
        }
    })

    it.each(ALL_SPECS)('%s: 固定列在两端(左固定第一列、右固定最后一列)', (_name, specs) => {
        const left = specs.filter((spec) => spec.fixed === 'left')
        const right = specs.filter((spec) => spec.fixed === 'right')
        expect(left).toHaveLength(1)
        expect(right).toHaveLength(1)
        expect(specs[0].fixed).toBe('left')
        expect(specs[specs.length - 1].fixed).toBe('right')
    })

    it('数字列右对齐(一列数字要像一列数字)', () => {
        for (const key of ['recall', 'rr']) {
            expect(CASE_TABLE_SPECS.find((spec) => spec.key === key)?.align).toBe('right')
        }
        expect(WORST_TABLE_SPECS.find((spec) => spec.key === 'recall')?.align).toBe('right')
    })

    it('紧凑表头(断言/rubric/主因)都配了口径提示', () => {
        for (const key of ['claims', 'rubric', 'primary_flag']) {
            expect(CASE_TABLE_SPECS.find((spec) => spec.key === key)?.hint, `${key} 缺表头提示`).toBeTruthy()
        }
    })
})

describe('scrollX', () => {
    it('固定列宽之和 + 自适应列下限', () => {
        expect(fixedWidthTotal(CASE_TABLE_SPECS)).toBe(96 + 64 + 82 + 72 + 132 + 96 + 76 + 180 + 92)
        expect(CASE_TABLE_SCROLL_X).toBe(fixedWidthTotal(CASE_TABLE_SPECS) + FLEX_MIN_WIDTH)
        expect(WORST_TABLE_SCROLL_X).toBe(fixedWidthTotal(WORST_TABLE_SPECS) + FLEX_MIN_WIDTH)
    })

    it('加一列自动变宽(以前这里要手改常数, 忘了就把列挤窄)', () => {
        const grown = scrollX([...CASE_TABLE_SPECS, { key: 'extra', title: '新列', width: 100 }])
        expect(grown).toBe(CASE_TABLE_SCROLL_X + 100)
    })

    it('scroll-x 不小于所有列宽的下限之和(不然一定有列被压到装不下)', () => {
        const minTotal = CASE_TABLE_SPECS.reduce(
            (sum, spec) => sum + (spec.width ?? (spec.minWidth as number)),
            0,
        )
        expect(CASE_TABLE_SCROLL_X).toBeGreaterThanOrEqual(minTotal)
    })
})

describe('buildColumns', () => {
    it('按规格顺序产出, 并带上宽/固定/对齐/省略', () => {
        const cols = buildColumns(CASE_TABLE_SPECS, {})
        expect(cols).toHaveLength(CASE_TABLE_SPECS.length)
        expect((cols as DataTableBaseColumn<RunCaseResult>[]).map((col) => String(col.key))).toEqual(
            CASE_TABLE_SPECS.map((spec) => spec.key),
        )

        const recall = columnOf(cols, 'recall')
        expect(recall.width).toBe(82)
        expect(recall.align).toBe('right')
        expect(recall.fixed).toBeUndefined()

        const qid = columnOf(cols, 'qid')
        expect(qid.fixed).toBe('left')

        const question = columnOf(cols, 'question')
        expect(question.width).toBeUndefined()
        expect(question.ellipsis).toEqual({ tooltip: true })
    })

    it('只有标了 hint 的列才有表头提示(说明放 tooltip, 表头文字不变长)', () => {
        const cols = buildColumns(CASE_TABLE_SPECS, {})
        const claims = columnOf(cols, 'claims')
        expect(claims.title).toBeTypeOf('function')
        // 渲染出来的是个 tooltip 节点, 而不是把说明拼进标题文字
        const node = (claims.title as unknown as () => { type: unknown })()
        expect(node.type).toBe(NTooltip)
        expect(columnOf(cols, 'qid').title).toBe('qid')
    })

    it('渲染函数缺失时给空字符串, 不抛错(表格不该因为少写一个 render 白屏)', () => {
        const cols = buildColumns([{ key: 'unknown', title: '未知' }], {})
        expect(renderText(cols, 'unknown', caseRow())).toBe('')
    })
})

describe('两张表的渲染函数都齐全', () => {
    const handlers = { openDetail: () => {} }

    it.each([
        ['全量单题结果表', CASE_TABLE_SPECS, caseTableColumns(handlers)],
        ['最差用例表', WORST_TABLE_SPECS, worstTableColumns(handlers)],
    ] as [string, ColumnSpec[], DataTableColumns<RunCaseResult>][])(
        '%s: 每个 key 都有 render',
        (_name, specs, cols) => {
            for (const spec of specs) {
                expect(columnOf(cols, spec.key).render, `${spec.key} 没有 render`).toBeTypeOf('function')
            }
        },
    )

    it('数值/文本列渲染成可读文本', () => {
        const cols = caseTableColumns(handlers)
        const row = caseRow({
            metrics: { recall: 0.6736, reciprocal_rank: 0.7833, first_hit_rank: 2 },
            answer: '答案正文',
        })
        expect(renderText(cols, 'qid', row)).toBe('Q001')
        expect(renderText(cols, 'recall', row)).toBe('67.4%')
        expect(renderText(cols, 'rr', row)).toBe('0.783')
        expect(renderText(cols, 'answer', row)).toBe('答案正文')
        // 没命中的题必须说"未命中"而不是空白
        expect(renderText(worstTableColumns(handlers), 'first_hit_rank', caseRow())).toBe('未命中')
        expect(renderText(worstTableColumns(handlers), 'first_hit_rank', row)).toBe('第 2 位')
    })

    it('答案为空时给占位节点而不是空白单元格', () => {
        const cols = caseTableColumns(handlers)
        const cell = columnOf(cols, 'answer').render?.(caseRow(), 0) as unknown as { type: unknown }
        expect(cell.type).toBe(NText)
    })
})

describe('claimsCell', () => {
    it('没有判定时给占位节点(不是 0/0/0)', () => {
        const cell = claimsCell(caseRow()) as unknown as { type: unknown }
        expect(cell.type).toBe(NText)
    })

    it('有断言时给"支持/无据/无关"三段计数', () => {
        const row = caseRow({
            judge: {
                claims: [
                    { id: 1, text: 'a', label: 'supported' },
                    { id: 2, text: 'b', label: 'unsupported' },
                ],
                rubric: null,
                meta: {},
            },
        })
        const cell = claimsCell(row) as unknown as { children: unknown; props: { title: string; style: string } }
        expect(cell.children).toBe('1/1/0')
        expect(cell.props.title).toBe('1 有据 / 1 无据 / 0 无关')
        // 出现"无据"必须标红 —— 用的是语义色板里的错误色, 不是另抄一个红
        expect(cell.props.style).toContain(tagHex('error'))
    })

    it('全是支持的断言用弱化文字色(不抢眼)', () => {
        const row = caseRow({
            judge: { claims: [{ id: 1, text: 'a', label: 'supported' }], rubric: null, meta: {} },
        })
        const cell = claimsCell(row) as unknown as { props: { style: string } }
        expect(cell.props.style).not.toContain(tagHex('error'))
        expect(cell.props.style).toContain('rgba(')
    })
})
