import { describe, expect, it } from 'vitest'
import type { GoldBinaryCalibration, JudgeCalibration } from '../api/types'
import {
    biasText,
    disagreementEmptyHint,
    disagreementLabel,
    disagreementSummary,
    disagreementTagType,
    goldReviewText,
    reviewTagType,
    binaryConfusionCells,
    calibrationVerdict,
    confusionMax,
    coverageText,
    goldProgressText,
    goldShortcut,
    goldVerdictLabel,
    goldVerdictType,
    heatIntensity,
    interAnnotatorText,
    isCalibrationUsable,
    kappaBand,
    noteSeverity,
    notesWithSeverity,
    scoreLine,
} from './calibration'

function binary(overrides: Partial<GoldBinaryCalibration> = {}): GoldBinaryCalibration {
    return {
        pairs: 30,
        judge_positive: 3,
        human_positive: 5,
        true_positive: 2,
        false_positive: 1,
        true_negative: 25,
        false_negative: 3,
        agreement: 0.9,
        kappa: 0.44,
        excluded_unclear: 2,
        ...overrides,
    }
}

function report(overrides: Partial<JudgeCalibration> = {}): JudgeCalibration {
    return {
        run_id: 155,
        total_cases: 60,
        cases_with_gold: 32,
        primary_annotator: 'me',
        annotators: ['me'],
        binary: binary(),
        coverage: 0.5333,
        gold_judged: 32,
        gold_unjudged: 0,
        gold_reviewed: 0,
        disagreements: [],
        notes: [],
        ...overrides,
    }
}

describe('gold verdict', () => {
    it('三值都有中文与配色, 缺失时说"未判定"', () => {
        expect(goldVerdictLabel('faithful')).toBe('忠实(有据)')
        expect(goldVerdictLabel('hallucinated')).toBe('有幻觉(无据)')
        expect(goldVerdictLabel('unclear')).toBe('看不清')
        expect(goldVerdictLabel(undefined)).toBe('未判定')
        expect(goldVerdictType('hallucinated')).toBe('error')
        expect(goldVerdictType('unclear')).toBe('default')
        expect(goldVerdictType(undefined)).toBe('default')
    })

    it('进度文字用"已标 X/Y"', () => {
        expect(goldProgressText(30, 12)).toBe('已标 12/30')
    })
})

describe('goldReviewText / reviewTagType', () => {
    it('复核进度报"已复核 X/Y", 没有金标时说清', () => {
        expect(goldReviewText([{ reviewed: true }, { reviewed: false }, { reviewed: true }], 3))
            .toBe('已复核 2/3')
        expect(goldReviewText([], 0)).toBe('还没有可复核的金标')
    })

    it('只有复核过才给绿色 —— 未复核用灰色, 不暗示"标错了"', () => {
        expect(reviewTagType(true)).toBe('success')
        expect(reviewTagType(false)).toBe('default')
        expect(reviewTagType(undefined)).toBe('default')
    })
})

describe('goldShortcut', () => {
    it('1/2/3 判词, n/b 换题', () => {
        expect(goldShortcut('1')).toEqual({ kind: 'verdict', value: 'faithful' })
        expect(goldShortcut('2')).toEqual({ kind: 'verdict', value: 'hallucinated' })
        expect(goldShortcut('3')).toEqual({ kind: 'verdict', value: 'unclear' })
        expect(goldShortcut('n')).toEqual({ kind: 'next' })
        expect(goldShortcut('B')).toEqual({ kind: 'prev' })
    })

    it('数字 4/5 不给快捷键 —— 星级只走鼠标, 免得按错还要回头找', () => {
        expect(goldShortcut('4')).toBeNull()
        expect(goldShortcut('5')).toBeNull()
        expect(goldShortcut('x')).toBeNull()
    })
})

describe('kappaBand', () => {
    it('按 Landis & Koch 分档, 并给出"这意味着什么"', () => {
        expect(kappaBand(0.85).label).toBe('几乎完全一致')
        expect(kappaBand(0.85).type).toBe('success')
        expect(kappaBand(0.65).label).toBe('一致性较好')
        expect(kappaBand(0.5).label).toBe('中等一致')
        expect(kappaBand(0.5).type).toBe('warning')
        expect(kappaBand(0.3).label).toBe('一致性偏低')
        expect(kappaBand(0.1).label).toBe('几乎没有一致性')
        expect(kappaBand(0.1).type).toBe('error')
        expect(kappaBand(-0.2).label).toBe('比瞎猜还差')
    })

    it('κ 无定义(没有变异)时不能显示成一个数字', () => {
        expect(kappaBand(undefined).label).toBe('无法计算')
        expect(kappaBand(Number.NaN).label).toBe('无法计算')
    })
})

describe('calibrationVerdict', () => {
    it('没有金标时先让人去打分', () => {
        const verdict = calibrationVerdict(report({ cases_with_gold: 0, binary: undefined }))
        expect(verdict.type).toBe('warning')
        expect(verdict.text).toContain('还没有人工金标')
    })

    it('有金标但没有可配对的判定: 说清是"没有判定", 不是"没有幻觉"', () => {
        const verdict = calibrationVerdict(report({ binary: undefined }))
        expect(verdict.type).toBe('warning')
        expect(verdict.text).toContain('没有可配对的判定结论')
    })

    it('样本 <20 时把"置信区间宽"挂在结论后面', () => {
        const verdict = calibrationVerdict(report({ binary: binary({ pairs: 4, kappa: 1, agreement: 1 }) }))
        expect(verdict.text).toContain('κ=1.00')
        expect(verdict.text).toContain('样本只有 4 题')
        // κ=1 的档位本身是 success, 但结论里已经说明了样本不足
        expect(verdict.type).toBe('success')
    })

    it('样本充足时结论只讲事实, 不加样本警告', () => {
        const verdict = calibrationVerdict(report())
        expect(verdict.text).toContain('一致率 90.0%')
        expect(verdict.text).not.toContain('样本只有')
    })
})

describe('binaryConfusionCells', () => {
    it('四格固定顺序: 命中/漏判/误报/正确放行', () => {
        const cells = binaryConfusionCells(binary())
        expect(cells.map((cell) => cell.key)).toEqual(['tp', 'fn', 'fp', 'tn'])
        expect(cells.map((cell) => cell.count)).toEqual([2, 3, 1, 25])
        // "漏判"是红: judge 放过了人工认定的幻觉, 这是最该被看见的错法
        expect(cells[1].type).toBe('error')
        expect(cells[1].detail).toContain('人工说有幻觉')
    })
})

describe('scoreLine / biasText', () => {
    it('分数校准一句话总结', () => {
        const line = scoreLine({
            pairs: 30, exact_agreement: 0.5, within_1: 0.9, mae: 0.63,
            judge_mean: 4.2, human_mean: 3.9, bias: 0.3, confusion: [],
        })
        expect(line).toContain('30 对样本')
        expect(line).toContain('完全一致 50%')
        expect(line).toContain('±1 以内 90%')
        expect(line).toContain('MAE 0.63')
    })

    it('没有样本时不给假数字', () => {
        expect(scoreLine(undefined)).toBe('没有可比对的分数样本')
        expect(scoreLine(null)).toBe('没有可比对的分数样本')
    })

    it('偏差带符号: 正数 = judge 偏宽松', () => {
        expect(biasText(0.3)).toBe('judge 偏宽松 0.30 分')
        expect(biasText(-0.5)).toBe('judge 偏严格 0.50 分')
        expect(biasText(0.01)).toBe('基本无偏')
        expect(biasText(undefined)).toBe('—')
    })
})

describe('heatIntensity / confusionMax', () => {
    it('相对最大值的强度, 0 与空表都不上色', () => {
        expect(heatIntensity(5, 10)).toBe(0.5)
        expect(heatIntensity(0, 10)).toBe(0)
        expect(heatIntensity(3, 0)).toBe(0)
        expect(heatIntensity(20, 10)).toBe(1)
    })

    it('取 5×5 里的最大值', () => {
        const confusion = [
            [0, 0, 0, 0, 0],
            [0, 2, 0, 0, 0],
            [0, 0, 7, 0, 0],
            [0, 0, 0, 1, 0],
            [0, 0, 0, 0, 3],
        ]
        expect(confusionMax({
            pairs: 13, exact_agreement: 1, within_1: 1, mae: 0,
            judge_mean: 3, human_mean: 3, bias: 0, confusion,
        })).toBe(7)
    })
})

describe('interAnnotatorText', () => {
    it('两人都有样本时并排给一致性', () => {
        const text = interAnnotatorText(report({
            inter_annotator: {
                annotators_a: 'anna', annotators_b: 'bob',
                binary_pairs: 12, binary_kappa: 0.61,
                score_pairs: 10, score_mae: 0.3, score_exact_rate: 0.7,
            },
        }))
        expect(text).toContain('anna vs bob')
        expect(text).toContain('κ=0.61')
        expect(text).toContain('12 题重合')
        expect(text).toContain('分数 MAE 0.30')
    })

    it('重合题不足 2 时不给"一致性"这个假数字', () => {
        const text = interAnnotatorText(report({
            inter_annotator: {
                annotators_a: 'anna', annotators_b: 'bob',
                binary_pairs: 0, score_pairs: 1,
            },
        }))
        expect(text).toContain('重合题不足 2 道')
    })

    it('只有一位标注员时没有这一段', () => {
        expect(interAnnotatorText(report())).toBeNull()
    })
})

describe('判错清单', () => {
    const items = [
        { case_id: 3, qid: 'zjc-003', kind: 'missed' as const, human_verdict: 'hallucinated' as const, judge_unsupported: 0 },
        { case_id: 9, qid: 'zjc-009', kind: 'missed' as const, human_verdict: 'hallucinated' as const, judge_unsupported: 0 },
        { case_id: 5, qid: 'zjc-005', kind: 'false_alarm' as const, human_verdict: 'faithful' as const, judge_unsupported: 2 },
    ]

    it('漏判是红、误报是黄 —— 两种错的代价不一样', () => {
        expect(disagreementTagType('missed')).toBe('error')
        expect(disagreementTagType('false_alarm')).toBe('warning')
        expect(disagreementLabel('missed')).toContain('漏判')
        expect(disagreementLabel('false_alarm')).toContain('误报')
    })

    it('总结先报漏判, 再报误报', () => {
        const text = disagreementSummary(items)
        expect(text).toContain('漏判 2 题')
        expect(text).toContain('误报 1 题')
        expect(text).toContain('共 3 题不一致')
    })

    it('没有不一致时是好消息, 而不是空表', () => {
        expect(disagreementSummary([])).toBe('没有发现人机不一致的题')
        expect(disagreementEmptyHint(report())).toContain('完全一致')
    })

    it('没有可配对样本时不许说"完全一致"', () => {
        expect(disagreementEmptyHint(report({ binary: undefined }))).toContain('谈不上')
    })
})

describe('coverageText / isCalibrationUsable', () => {
    it('覆盖率用 X/Y 展示', () => {
        expect(coverageText(report())).toBe('32/60')
    })

    it('样本<20 或覆盖率<50% 就不算可用结论', () => {
        expect(isCalibrationUsable(report())).toBe(true)
        expect(isCalibrationUsable(report({ binary: binary({ pairs: 12 }) }))).toBe(false)
        expect(isCalibrationUsable(report({ coverage: 0.4 }))).toBe(false)
        expect(isCalibrationUsable(report({ binary: undefined }))).toBe(false)
    })
})

describe('notesWithSeverity', () => {
    it('样本量/覆盖率/单人标注是"别信我"级别, 其余是背景说明', () => {
        expect(noteSeverity('金标覆盖率低于 50%: 校准结论只能代表被标注的那部分题')).toBe('warning')
        expect(noteSeverity('参与二分类校准的题数少于 20 题: κ 的置信区间很宽')).toBe('warning')
        expect(noteSeverity('只有一位标注员: 无法计算人工之间的一致性')).toBe('warning')
        expect(noteSeverity('有 2 题人工判为"看不清", 已排除出二分类样本')).toBe('info')
    })

    it('逐条带上严重程度', () => {
        const items = notesWithSeverity(['覆盖率低于 50%: x', '有 1 题 judge 没有给出判定结论'])
        expect(items[0].type).toBe('warning')
        expect(items[1].type).toBe('info')
    })
})
