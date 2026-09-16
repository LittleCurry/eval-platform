// A/B 对比的展示口径与导出(M5-1)。纯函数, 便于单测。

import type { ABCaseDelta, ABReport, ABStratum } from '../api/types'
import type { TagType } from './format'
import { flagLabel } from './report'

/** 指标 key -> 人话标签(与 worker 侧 CaseMetric 字段名对齐)。 */
export const METRIC_LABELS: Record<string, string> = {
    recall: 'Recall@k',
    reciprocal_rank: 'MRR@k',
    hit: 'Hit@k',
    precision: 'Precision@k',
}

export function metricLabel(metric: string): string {
    return METRIC_LABELS[metric] ?? metric
}

/** 差值按"百分点"显示: 0.275 -> "+27.5pp"(检索指标都是 0~1 的比例)。 */
export function formatDelta(delta: number | undefined, digits = 1): string {
    if (delta === undefined || Number.isNaN(delta)) return '—'
    const sign = delta > 0 ? '+' : ''
    return `${sign}${(delta * 100).toFixed(digits)}pp`
}

/**
 * 差值的三态配色: 落在噪声底内一律灰(不宣称变化) —— 这是 M5 最核心的展示纪律,
 * 否则用户会把跨 run 抖动当成"优化有效"。
 */
export function deltaTagType(delta: number | undefined, noiseFloor: number): TagType {
    if (delta === undefined || Number.isNaN(delta)) return 'default'
    if (Math.abs(delta) <= noiseFloor) return 'default'
    return delta > 0 ? 'success' : 'error'
}

/** 标签迁移的人话: "检索漏召回(全未命中) → 干净"。 */
export function flagTransitionText(row: ABCaseDelta, labelOf: (flag: string) => string): string {
    const left = row.flags_left.length > 0 ? row.flags_left.map(labelOf).join(' + ') : '干净'
    const right = row.flags_right.length > 0 ? row.flags_right.map(labelOf).join(' + ') : '干净'
    return `${left} → ${right}`
}

/** 分层行的"标签数变化": "13 → 0"。 */
export function stratumFlagShift(row: ABStratum): string {
    if (row.flagged_left === 0 && row.flagged_right === 0) return '—'
    return `${row.flagged_left} → ${row.flagged_right}`
}

/** CSV 单元格转义: 题目里出现逗号/引号/换行时必须包起来并翻倍引号。 */
function csvCell(value: unknown): string {
    const text = value === undefined || value === null ? '' : String(value)
    if (/[",\n\r]/.test(text)) return `"${text.replace(/"/g, '""')}"`
    return text
}

function csvRows(rows: unknown[][]): string {
    return rows.map((row) => row.map(csvCell).join(',')).join('\n')
}

/**
 * 导出 CSV: 三段(汇总 / 翻转题 / 分层)拼在一个文件里, 用空行区隔。
 * 这样同事双击打开就能看完, 不必下三个文件。
 */
export function buildCompareCsv(report: ABReport): string {
    const blocks: string[] = []

    blocks.push(csvRows([
        ['对比', `run ${report.left}`, `run ${report.right}`],
        ['共同题目', report.shared_cases, ''],
        ['噪声底', report.noise_floor, ''],
        ['可比', report.comparable ? '是' : `否(${report.reason ?? ''})`, ''],
        ['指标', '左', '右', '差值', '改善题数', '恶化题数', 'p值', '是否显著', '是否落在噪声底内'],
        ...Object.entries(report.summary).map(([metric, delta]) => [
            metricLabel(metric),
            delta.left,
            delta.right,
            delta.delta,
            delta.improved,
            delta.worsened,
            delta.p_value,
            delta.significant ? '是' : '否',
            delta.below_noise ? '是' : '否',
        ]),
    ]))

    const transitions = (rows: ABCaseDelta[], kind: string) => rows.map((row) => [
        kind,
        row.qid,
        row.category ?? '',
        row.difficulty ?? '',
        row.flags_left.join(' + ') || '干净',
        row.flags_right.join(' + ') || '干净',
    ])
    blocks.push(csvRows([
        ['翻转题', 'qid', '类别', '难度', '左侧标签', '右侧标签'],
        ...transitions(report.fixed, '修好'),
        ...transitions(report.broke, '变坏'),
        ...transitions(report.changed, '标签变化'),
    ]))

    const strata = (rows: ABStratum[], kind: string) => rows.map((row) => [
        kind, row.key, row.cases, row.mean_delta, row.improved, row.worsened, row.flagged_left, row.flagged_right,
    ])
    blocks.push(csvRows([
        ['分层', '分组', '题数', 'Recall 均值差', '改善', '恶化', '左侧有标签', '右侧有标签'],
        ...strata(report.by_category, '类别'),
        ...strata(report.by_difficulty, '难度'),
        ...strata(report.by_flag, '归因标签'),
    ]))

    return blocks.join('\n\n')
}

/** 导出 Markdown: 直接贴进飞书/PR 就能读。 */
export function buildCompareMarkdown(report: ABReport): string {
    const lines: string[] = []
    lines.push(`# A/B 对比：run #${report.left}（左）vs run #${report.right}（右）`)
    if (!report.comparable) {
        lines.push('', `> ⚠️ 不可比：${report.reason ?? '未知原因'}`)
        return lines.join('\n')
    }
    lines.push(
        '',
        `共同题目 ${report.shared_cases} 题（左 ${report.left_cases} / 右 ${report.right_cases}）｜差值小于 ${report.noise_floor} 视为落在噪声里`,
    )
    if (report.attribution_missing) {
        lines.push('', `> ⚠️ ${report.attribution_missing}`)
    }

    lines.push('', '## 指标', '', '| 指标 | 左 | 右 | 差值 | 改善 | 恶化 | p 值 | 结论 |', '|---|---|---|---|---|---|---|---|')
    for (const [metric, delta] of Object.entries(report.summary)) {
        const verdict = delta.below_noise ? '噪声内' : delta.significant ? '显著' : '不显著'
        lines.push(
            `| ${metricLabel(metric)} | ${delta.left} | ${delta.right} | ${formatDelta(delta.delta)} | `
            + `${delta.improved} | ${delta.worsened} | ${delta.p_value} | ${verdict} |`,
        )
    }

    // Markdown 是给人读的 -> 标签翻成中文; CSV 反过来保留原始 key 方便脚本筛
    const transition = (rows: ABCaseDelta[]) => rows.map((row) => {
        const left = row.flags_left.map(flagLabel).join(' + ') || '干净'
        const right = row.flags_right.map(flagLabel).join(' + ') || '干净'
        return `- ${row.qid}：${left} → ${right}`
    })
    lines.push('', `## 修好的题（${report.fixed.length}）`)
    lines.push(...(report.fixed.length ? transition(report.fixed) : ['- 无']))
    lines.push('', `## 变坏的题（${report.broke.length}）`)
    lines.push(...(report.broke.length ? transition(report.broke) : ['- 无']))
    if (report.changed.length) {
        lines.push('', `## 标签变化但不好不坏（${report.changed.length}）`)
        lines.push(...transition(report.changed))
    }

    const strata = (title: string, rows: ABStratum[]) => {
        if (!rows.length) return
        lines.push('', `## ${title}`, '', '| 分组 | 题数 | Recall 均值差 | 改善 | 恶化 | 有标签(左→右) |', '|---|---|---|---|---|---|')
        for (const row of rows) {
            lines.push(
                `| ${row.key} | ${row.cases} | ${formatDelta(row.mean_delta)} | ${row.improved} | `
                + `${row.worsened} | ${stratumFlagShift(row)} |`,
            )
        }
    }
    strata('按类别', report.by_category)
    strata('按难度', report.by_difficulty)
    strata('按归因标签', report.by_flag)

    lines.push('', '---', '', '> 怎么读：结论以「噪声底 + 翻转题清单」为准；p 值只作旁证 —— '
        + '检索指标离散且大量并列（recall 常只有 0/0.5/1），Wilcoxon 的正态近似在大量同分下并不可靠。')
    return lines.join('\n')
}

/** 导出文件名: 带上两侧 run id 与日期, 免得下完就分不清。 */
export function exportFilename(left: number, right: number, ext: string, now = new Date()): string {
    const pad = (value: number) => String(value).padStart(2, '0')
    const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}`
    return `ab-${left}-vs-${right}-${stamp}.${ext}`
}
