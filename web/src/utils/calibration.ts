// judge 校准的展示口径(M6-3)。纯函数, 便于单测。
//
// 这一屏最容易犯的错是"报一个漂亮数字就完事": 4 题金标也能算出 κ=1。
// 所以这里所有函数都围绕一件事 —— **把不可信的原因顶到人眼前**。

import type {
    CalibrationDisagreement,
    GoldBinaryCalibration,
    GoldScoreCalibration,
    JudgeCalibration,
} from '../api/types'
import type { TagType } from './format'

/** 三值判定的中文与配色: "看不清"用灰色, 不给人"选它更省事"的暗示。 */
export const GOLD_VERDICTS = ['faithful', 'hallucinated', 'unclear'] as const

export const GOLD_VERDICT_LABELS: Record<string, string> = {
    faithful: '忠实(有据)',
    hallucinated: '有幻觉(无据)',
    unclear: '看不清',
}

export const GOLD_VERDICT_TYPES: Record<string, TagType> = {
    faithful: 'success',
    hallucinated: 'error',
    unclear: 'default',
}

export function goldVerdictLabel(verdict?: string | null): string {
    if (!verdict) return '未判定'
    return GOLD_VERDICT_LABELS[verdict] ?? verdict
}

export function goldVerdictType(verdict?: string | null): TagType {
    if (!verdict) return 'default'
    return GOLD_VERDICT_TYPES[verdict] ?? 'default'
}

/** 打分进度: "已标 12/30"。分母是这次 run 参与打分的题数(不是全部题)。 */
export function goldProgressText(total: number, scored: number): string {
    return `已标 ${scored}/${total}`
}

/**
 * 快捷键: 1/2/3 判词, n 下一题, b 上一题。
 *
 * 为什么分数不给快捷键: 数字键已经被判词占了, 把 4/5/6 映射成星级是"看起来更快、
 * 实际更容易按错"的设计 —— 打错的分要回头找, 反而更慢。星级用鼠标点。
 */
export function goldShortcut(key: string): { kind: 'verdict' | 'next' | 'prev'; value?: string } | null {
    switch (key) {
        case '1':
            return { kind: 'verdict', value: 'faithful' }
        case '2':
            return { kind: 'verdict', value: 'hallucinated' }
        case '3':
            return { kind: 'verdict', value: 'unclear' }
        case 'n':
        case 'N':
            return { kind: 'next' }
        case 'b':
        case 'B':
            return { kind: 'prev' }
        default:
            return null
    }
}

export const GOLD_SHORTCUT_HELP =
    '快捷键：1 忠实(有据) ｜ 2 有幻觉(无据) ｜ 3 看不清 ｜ n 下一题 ｜ b 上一题。' +
    '判词是必填, 分数(1–5 星)可以后补 —— 先判有没有幻觉, 再回头补分。'

/**
 * 复核进度: "已复核 3/12"。
 *
 * 为什么要单独报这一个数: 单人未复核的金标只能证明"这个人 vs judge"一致,
 * 不能证明"人工 vs judge"一致 —— 换个人标一遍结论可能就变了。
 */
export function goldReviewText(golds: { reviewed: boolean }[], annotated: number): string {
    const reviewed = golds.filter((item) => item.reviewed).length
    if (annotated === 0) return '还没有可复核的金标'
    return `已复核 ${reviewed}/${annotated}`
}

/** 复核标记的配色: 复核过才给绿色, 否则灰色(不暗示"没复核=不好")。 */
export function reviewTagType(reviewed?: boolean): TagType {
    return reviewed ? 'success' : 'default'
}

/** κ 的解读档位(Landis & Koch 的口径, 但每档都配一句"这意味着什么")。 */
export interface KappaBand {
    label: string
    type: TagType
    hint: string
}

export function kappaBand(kappa?: number | null): KappaBand {
    if (kappa === undefined || kappa === null || Number.isNaN(kappa)) {
        return { label: '无法计算', type: 'default', hint: '样本里没有变异(两侧都只出现一种判定), κ 无定义' }
    }
    if (kappa < 0) {
        return { label: '比瞎猜还差', type: 'error', hint: 'judge 与人工系统性相反 —— 先别用它做结论' }
    }
    if (kappa < 0.2) {
        return { label: '几乎没有一致性', type: 'error', hint: 'judge 相对"永远说没错"几乎没有额外信息量' }
    }
    if (kappa < 0.4) {
        return { label: '一致性偏低', type: 'warning', hint: '只能当粗略方向, 不要拿它给答案打勾' }
    }
    if (kappa < 0.6) {
        return { label: '中等一致', type: 'warning', hint: '可用于筛 bad case, 具体结论仍需人工确认' }
    }
    if (kappa < 0.8) {
        return { label: '一致性较好', type: 'success', hint: 'judge 与人工基本对得上, 可以承担初筛' }
    }
    return { label: '几乎完全一致', type: 'success', hint: 'judge 在这个数据集上很可信' }
}

/** 顶部那句结论: 让人一眼知道"这份报告能不能用"。 */
export function calibrationVerdict(report: JudgeCalibration): { type: TagType; text: string } {
    if (report.cases_with_gold === 0) {
        return { type: 'warning', text: '还没有人工金标: 先去打分页挑 30–50 题判一遍' }
    }
    if (!report.binary) {
        return {
            type: 'warning',
            text: `有 ${report.cases_with_gold} 题金标, 但没有可配对的判定结论` +
                '（答案没有可核查断言, 或这次 run 没开 judge）—— 无法校准幻觉判定',
        }
    }
    const band = kappaBand(report.binary.kappa)
    const sizeNote = report.binary.pairs < 20
        ? `；样本只有 ${report.binary.pairs} 题(<20), κ 的置信区间很宽`
        : ''
    return {
        type: band.type,
        text: `judge 与人工的幻觉判定：κ=${report.binary.kappa.toFixed(2)}（${band.label}），` +
            `一致率 ${(report.binary.agreement * 100).toFixed(1)}%${sizeNote}`,
    }
}

/** 二分类的四个格子(固定顺序, 前端按 2×2 摆)。 */
export interface ConfusionCell {
    key: string
    label: string
    detail: string
    count: number
    type: TagType
}

export function binaryConfusionCells(binary: GoldBinaryCalibration): ConfusionCell[] {
    return [
        {
            key: 'tp',
            label: '命中',
            detail: 'judge 说有问题, 人工也说是',
            count: binary.true_positive,
            type: 'success',
        },
        {
            key: 'fn',
            label: '漏判',
            detail: '人工说有幻觉, judge 说没问题',
            count: binary.false_negative,
            type: 'error',
        },
        {
            key: 'fp',
            label: '误报',
            detail: 'judge 说有幻觉, 人工说没问题',
            count: binary.false_positive,
            type: 'warning',
        },
        {
            key: 'tn',
            label: '正确放行',
            detail: '两侧都说没问题',
            count: binary.true_negative,
            type: 'success',
        },
    ]
}

/** 分数校准的一句话总结: MAE 是主角, 一致性率是配角(差 1 分其实常常可以接受)。 */
export function scoreLine(score?: GoldScoreCalibration | null): string {
    if (!score) return '没有可比对的分数样本'
    return `${score.pairs} 对样本 ｜ 完全一致 ${(score.exact_agreement * 100).toFixed(0)}% ｜ ` +
        `±1 以内 ${(score.within_1 * 100).toFixed(0)}% ｜ MAE ${score.mae.toFixed(2)}`
}

/** 均值偏差: 带符号, 让人看出 judge 偏宽松还是偏严格。 */
export function biasText(bias?: number | null): string {
    if (bias === undefined || bias === null || Number.isNaN(bias)) return '—'
    if (Math.abs(bias) < 0.05) return '基本无偏'
    const value = Math.abs(bias).toFixed(2)
    return bias > 0 ? `judge 偏宽松 ${value} 分` : `judge 偏严格 ${value} 分`
}

/** 覆盖率: 有金标的题占全部题的比例(结论能不能外推全看它)。 */
export function coverageText(report: JudgeCalibration): string {
    return `${report.cases_with_gold}/${report.total_cases}`
}

/**
 * 热力图强度: 0~1, 供前端给 5×5 格子配色。
 * 用出现次数 / 该表最大值; 全 0 时返回 0(别把"没有样本"涂成深色)。
 */
export function heatIntensity(count: number, max: number): number {
    if (max <= 0 || count <= 0) return 0
    return Math.min(1, count / max)
}

export function confusionMax(score: GoldScoreCalibration): number {
    let max = 0
    for (const row of score.confusion) {
        for (const cell of row) {
            if (cell > max) max = cell
        }
    }
    return max
}

/** 人工之间的一致性: 只有两人都打过分的题才算, 且样本 <2 不给结论。 */
export function interAnnotatorText(report: JudgeCalibration): string | null {
    const inter = report.inter_annotator
    if (!inter) return null
    const parts: string[] = []
    if (inter.binary_kappa !== undefined) {
        parts.push(`幻觉判定 κ=${inter.binary_kappa.toFixed(2)}（${inter.binary_pairs} 题重合）`)
    }
    if (inter.score_mae !== undefined) {
        parts.push(`分数 MAE ${inter.score_mae.toFixed(2)}（${inter.score_pairs} 题重合）`)
    }
    if (parts.length === 0) {
        return `${inter.annotators_a} 与 ${inter.annotators_b} 的重合题不足 2 道, 还算不出一致性`
    }
    return `${inter.annotators_a} vs ${inter.annotators_b}：${parts.join('；')}`
}

/** 提示条的严重程度: 样本量/覆盖率的话是"别信我"级别, 其余是背景说明。 */
export function noteSeverity(note: string): TagType {
    if (note.includes('覆盖率') || note.includes('少于') || note.includes('只有一位')) {
        return 'warning'
    }
    return 'info'
}

export function notesWithSeverity(notes: string[]): { text: string; type: TagType }[] {
    return notes.map((text) => ({ text, type: noteSeverity(text) }))
}

/** 判错类型的中文与配色: 漏判是红(幻觉会流到线上), 误报是黄(只是多花人工)。 */
export function disagreementLabel(kind: string): string {
    return kind === 'missed' ? '漏判(judge 放过了幻觉)' : '误报(judge 冤枉了答案)'
}

export function disagreementTagType(kind: string): TagType {
    return kind === 'missed' ? 'error' : 'warning'
}

/**
 * 判错清单的一句话总结, 以及"先去看哪些"。
 *
 * 为什么把漏判单列: 误报只是多花人工, 漏判会让幻觉直接被当成正确答出去 ——
 * 改 prompt 的优先级完全不一样。
 */
export function disagreementSummary(items: CalibrationDisagreement[]): string {
    if (!items || items.length === 0) return '没有发现人机不一致的题'
    const missed = items.filter((item) => item.kind === 'missed').length
    const alarm = items.length - missed
    const parts: string[] = []
    if (missed > 0) parts.push(`漏判 ${missed} 题`)
    if (alarm > 0) parts.push(`误报 ${alarm} 题`)
    return `${parts.join(' ｜ ')}（共 ${items.length} 题不一致, 漏判排在前面）`
}

/** 判错清单为空时给一句"这其实是好消息", 而不是让人怀疑数据没加载出来。 */
export function disagreementEmptyHint(report: JudgeCalibration): string {
    if (!report.binary) return '没有可配对的判定结论, 还谈不上"判错"'
    return `已比对的 ${report.binary.pairs} 题里, judge 与人工的判定完全一致`
}

/** 金标是否足够支撑结论(页面顶部用来决定"结论卡"是否置灰)。 */
export function isCalibrationUsable(report: JudgeCalibration): boolean {
    return report.binary !== undefined && report.binary.pairs >= 20 && report.coverage >= 0.5
}