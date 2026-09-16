import { describe, expect, it } from 'vitest'
import type { ABReport } from '../api/types'
import {
    buildCompareCsv,
    buildCompareMarkdown,
    deltaTagType,
    exportFilename,
    flagTransitionText,
    formatDelta,
    isGenerationMetric,
    metricLabel,
    stratumFlagShift,
} from './compare'

function report(over: Partial<ABReport> = {}): ABReport {
    return {
        left: 112,
        right: 100,
        comparable: true,
        left_cases: 60,
        right_cases: 60,
        shared_cases: 60,
        noise_floor: 0.0021,
        summary: {
            recall: {
                left: 0.673611, right: 0.948611, delta: 0.275,
                improved: 20, worsened: 0, unchanged: 40,
                p_value: 0.000002, significant: true, below_noise: false,
                cases: 60, higher_is_better: true,
            },
        },
        fixed: [{ qid: 'zjc-003', category: '线索', difficulty: '易', flags_left: ['retrieval_miss'], flags_right: [] }],
        broke: [],
        changed: [],
        by_category: [{ key: '线索', cases: 40, mean_delta: 0.3, improved: 14, worsened: 0, flagged_left: 13, flagged_right: 0 }],
        by_difficulty: [],
        by_flag: [{ key: 'retrieval_miss', cases: 13, mean_delta: 1, improved: 13, worsened: 0, flagged_left: 13, flagged_right: 0 }],
        judged_cases: 60,
        ...over,
    }
}

const flagLabel = (flag: string) => ({ retrieval_miss: '检索漏召回(全未命中)', retrieval_low_rank: '命中但排序靠后' }[flag] ?? flag)

describe('metricLabel', () => {
    it('把内部字段名翻成人话', () => {
        expect(metricLabel('recall')).toBe('Recall@k')
        expect(metricLabel('reciprocal_rank')).toBe('MRR@k')
    })

    it('生成侧指标也有中文名', () => {
        expect(metricLabel('hallucination_rate')).toBe('幻觉率')
        expect(metricLabel('claim_support_rate')).toBe('断言支持率')
        expect(metricLabel('helpfulness')).toBe('helpfulness 均值')
    })

    it('未知指标原样返回', () => {
        expect(metricLabel('brand_new')).toBe('brand_new')
    })
})

describe('isGenerationMetric', () => {
    it('能把检索侧与生成侧分开(生成侧要额外提示样本量)', () => {
        expect(isGenerationMetric('hallucination_rate')).toBe(true)
        expect(isGenerationMetric('helpfulness')).toBe(true)
        expect(isGenerationMetric('recall')).toBe(false)
    })
})

describe('formatDelta', () => {
    it('按百分点显示并带正号', () => {
        expect(formatDelta(0.275)).toBe('+27.5pp')
        expect(formatDelta(-0.1)).toBe('-10.0pp')
        expect(formatDelta(0)).toBe('0.0pp')
    })

    it('缺失显示占位符', () => {
        expect(formatDelta(undefined)).toBe('—')
        expect(formatDelta(Number.NaN)).toBe('—')
    })
})

describe('deltaTagType', () => {
    it('落在噪声底内一律灰(不说变好变坏)', () => {
        expect(deltaTagType(0.001, 0.0021)).toBe('default')
        expect(deltaTagType(-0.002, 0.0021)).toBe('default')
    })

    it('超出噪声底才分好坏', () => {
        expect(deltaTagType(0.275, 0.0021)).toBe('success')
        expect(deltaTagType(-0.02, 0.0021)).toBe('error')
    })

    it('越低越好的指标方向要反过来(幻觉率下降是好事)', () => {
        expect(deltaTagType(-0.3, 0.0021, false)).toBe('success')
        expect(deltaTagType(0.3, 0.0021, false)).toBe('error')
        // 噪声底内的判定与方向无关
        expect(deltaTagType(-0.001, 0.0021, false)).toBe('default')
    })

    it('缺失返回 default', () => {
        expect(deltaTagType(undefined, 0.0021)).toBe('default')
    })
})

describe('flagTransitionText / stratumFlagShift', () => {
    it('把标签迁移写成人话', () => {
        expect(flagTransitionText(
            { qid: 'q1', flags_left: ['retrieval_miss'], flags_right: [] }, flagLabel,
        )).toBe('检索漏召回(全未命中) → 干净')
    })

    it('两侧都干净时也能读', () => {
        expect(flagTransitionText({ qid: 'q1', flags_left: [], flags_right: [] }, flagLabel)).toBe('干净 → 干净')
    })

    it('多标签用加号连接', () => {
        expect(flagTransitionText(
            { qid: 'q1', flags_left: ['retrieval_miss', 'hallucination'], flags_right: ['retrieval_low_rank'] },
            flagLabel,
        )).toBe('检索漏召回(全未命中) + hallucination → 命中但排序靠后')
    })

    it('分层行的标签数变化', () => {
        expect(stratumFlagShift({ key: 'k', cases: 13, mean_delta: 1, improved: 13, worsened: 0, flagged_left: 13, flagged_right: 0 })).toBe('13 → 0')
        expect(stratumFlagShift({ key: 'k', cases: 0, mean_delta: 0, improved: 0, worsened: 0, flagged_left: 0, flagged_right: 0 })).toBe('—')
    })
})

describe('buildCompareCsv', () => {
    it('三段内容都在, 且带表头', () => {
        const csv = buildCompareCsv(report())
        expect(csv).toContain('Recall@k')
        expect(csv).toContain('修好,qjc'.replace('qjc', 'zjc-003'))
        expect(csv).toContain('归因标签,retrieval_miss')
        expect(csv.split('\n\n')).toHaveLength(3)
    })

    it('含逗号/引号的字段被正确转义', () => {
        const csv = buildCompareCsv(report({
            fixed: [{ qid: 'zjc-001', category: '含,逗号', difficulty: '易', flags_left: ['a"b'], flags_right: [] }],
        }))
        expect(csv).toContain('"含,逗号"')
        expect(csv).toContain('"a""b"')
    })

    it('不可比时也给出原因', () => {
        const csv = buildCompareCsv(report({ comparable: false, reason: '不同评测集' }))
        expect(csv).toContain('否(不同评测集)')
    })
})

describe('buildCompareMarkdown', () => {
    it('含指标表与翻转题清单', () => {
        const markdown = buildCompareMarkdown(report())
        expect(markdown).toContain('# A/B 对比：run #112（左）vs run #100（右）')
        expect(markdown).toContain('| Recall@k | 0.673611 | 0.948611 | +27.5pp | 20 | 0 | 0.000002 | 显著（60 题） |')
        expect(markdown).toContain('- zjc-003：检索漏召回(全未命中) → 干净')
        expect(markdown).toContain('## 变坏的题（0）')
        expect(markdown).toContain('- 无')
    })

    it('把"p 值只作旁证"的读法写进去', () => {
        const markdown = buildCompareMarkdown(report())
        expect(markdown).toContain('p 值只作旁证')
    })

    it('噪声内的差值标成噪声内而不是显著', () => {
        const markdown = buildCompareMarkdown(report({
            summary: {
                recall: {
                    left: 0.9, right: 0.9009, delta: 0.0009,
                    improved: 0, worsened: 0, unchanged: 60,
                    p_value: 0.8, significant: false, below_noise: true,
                    cases: 60, higher_is_better: true,
                },
            },
        }))
        expect(markdown).toContain('噪声内')
    })

    it('不可比时直接给原因并停止输出表格', () => {
        const markdown = buildCompareMarkdown(report({ comparable: false, reason: '不同评测集' }))
        expect(markdown).toContain('不可比：不同评测集')
        expect(markdown).not.toContain('## 指标')
    })

    it('归因准备度不一致时写进报告', () => {
        const markdown = buildCompareMarkdown(report({ attribution_missing: '右侧 run 未做过归因' }))
        expect(markdown).toContain('右侧 run 未做过归因')
    })
})

describe('exportFilename', () => {
    it('带 run id 与日期, 便于事后分辨', () => {
        expect(exportFilename(112, 100, 'csv', new Date('2026-09-14T10:00:00'))).toBe('ab-112-vs-100-20260914.csv')
    })
})
