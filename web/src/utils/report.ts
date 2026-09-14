// 报告页的展示口径(纯函数, 便于单测; 测试环境是 node, 所以这里不碰 DOM)。
//
// 中文标签与 worker 侧 FLAG_ZH 一一对应 —— 唯一来源是 worker/app/eval/attribution.py。
// 两边各译一套就会出现"报告说幻觉、抽屉说无据"的错位, 所以改动那边时这里要同步。

import type { GenerationPayload, JudgePayload, RunMetrics } from '../api/types'
import type { TagType } from './format'

/** 归因标签的中文名(与 worker/app/eval/attribution.py 的 FLAG_ZH 同步)。 */
const FLAG_LABELS: Record<string, string> = {
    no_gold: '数据集缺 gold(不可评测)',
    anchor_incomplete: '锚点漏标(答案其实有据)',
    retrieval_miss: '检索漏召回(全未命中)',
    retrieval_partial: '检索部分漏召回',
    retrieval_low_rank: '命中但排序靠后',
    hallucination: '幻觉(有断言无据)',
    off_topic: '答非所问(断言与问题无关)',
    generation_quality: '生成质量不达标',
    no_claims: '无可核查断言',
}

/** 未知标签原样返回(别吞掉信息: 看到陌生标签应能去 attribution.py 里查)。 */
export function flagLabel(flag: string): string {
    return FLAG_LABELS[flag] ?? flag
}

/** 主因 = flags[0]: worker 侧已按优先级排好序(process.md D16), 读取方零成本推导。 */
export function primaryFlag(flags?: string[]): string | null {
    if (!flags || flags.length === 0) return null
    return flags[0] ?? null
}

/** 标签配色: 幻觉/不可评测是"真问题"(红), 检索类是"风险"(黄), 质量类是"信息"(蓝)。 */
export function flagTagType(flag?: string | null): TagType {
    if (!flag) return 'default'
    if (flag === 'hallucination' || flag === 'no_gold') return 'error'
    if (flag.startsWith('retrieval_')) return 'warning'
    if (flag === 'off_topic' || flag === 'generation_quality') return 'info'
    return 'default'
}

/** 断言计数文案; 没有判定或没有断言时返回 null(调用方显示占位符)。 */
export function claimSummary(judge?: JudgePayload | null): string | null {
    const claims = judge?.claims
    if (!claims || claims.length === 0) return null
    const count = (label: string) => claims.filter((claim) => claim.label === label).length
    return `${count('supported')} 支持 / ${count('unsupported')} 无据 / ${count('irrelevant')} 无关`
}

/** rubric 摘要: "R5 H4"; **没打分返回 null —— 不能显示成 0 分**(缺失 != 合格)。 */
export function rubricSummary(judge?: JudgePayload | null): string | null {
    const rubric = judge?.rubric
    if (!rubric) return null
    return `R${rubric.relevance} H${rubric.helpfulness}`
}

/** 断言标签的中文名。 */
export function claimLabel(label: string): string {
    switch (label) {
        case 'supported':
            return '有据'
        case 'unsupported':
            return '无据'
        case 'irrelevant':
            return '无关'
        default:
            return label
    }
}

export function claimTagType(label: string): TagType {
    switch (label) {
        case 'supported':
            return 'success'
        case 'unsupported':
            return 'error'
        case 'irrelevant':
            return 'warning'
        default:
            return 'default'
    }
}

/** 答案摘要(列表列用); 空白答案返回 null, 不显示空壳。 */
export function answerSnippet(answer?: string, length = 40): string | null {
    const text = (answer ?? '').replace(/\s+/g, ' ').trim()
    if (!text) return null
    return text.length <= length ? text : `${text.slice(0, length)}…`
}

/** 生成摘要(抽屉用): "deepseek-ai/DeepSeek-V3.2 · 634+24 tokens · 3.2s"。 */
export function generationSummary(generation?: GenerationPayload | null): string | null {
    if (!generation || !generation.model) return null
    const tokens = `${generation.prompt_tokens ?? 0}+${generation.completion_tokens ?? 0} tokens`
    const latency = generation.latency_ms ? ` · ${(generation.latency_ms / 1000).toFixed(1)}s` : ''
    return `${generation.model} · ${tokens}${latency}`
}

/** 是否展示「生成与判定」卡: 只跑检索的 run 不该看到一屏 0。 */
export function hasLlmMetrics(metrics?: RunMetrics): boolean {
    if (!metrics) return false
    return (metrics.cases_judged ?? 0) > 0 || (metrics.answers_generated ?? 0) > 0
}

/** 三率之和(分母都是 claims_total); 没有任何断言时返回 null(不是 0)。 */
export function ratesSum(metrics?: RunMetrics): number | null {
    if (!metrics || (metrics.claims_total ?? 0) <= 0) return null
    return (metrics.claim_support_rate ?? 0)
        + (metrics.hallucination_rate ?? 0)
        + (metrics.irrelevant_rate ?? 0)
}

/**
 * 三率自洽校验: 口径上必须等于 1。
 * 容差取 1e-5 而不是 1e-6 —— worker 侧每个率都四舍五入到 6 位小数, 三项相加最多偏 1.5e-6,
 * 卡太紧会天天误报。返回 null 表示"没有断言, 无从判断"。
 */
export function ratesConsistent(metrics?: RunMetrics, tolerance = 1e-5): boolean | null {
    const sum = ratesSum(metrics)
    if (sum === null) return null
    return Math.abs(sum - 1) <= tolerance
}

/** 归因口径文案(D15): 阈值不写清楚, 跨 run 的标签数就没法比。 */
export function attributionText(metrics?: RunMetrics): string | null {
    const attribution = metrics?.attribution
    if (!attribution) return null
    const parts = [
        `规则 v${attribution.version}`,
        `k=${attribution.k}`,
        `排序阈值 >${attribution.low_rank_limit}`,
        `达标线 ≤${attribution.quality_line}`,
        `覆盖 ${attribution.scope}`,
    ]
    return `归因口径：${parts.join(' · ')}`
}

/**
 * 未产出结果的条目提示。
 * 失败条目没有 case_results 行, 报告明细里根本看不到 —— 不提示的话,
 * "0 幻觉"会被这些没跑的题污染(看起来一片绿, 其实少跑了)。
 */
export function missingCaseNotice(failed?: number | null): string | null {
    if (!failed || failed <= 0) return null
    return `本次有 ${failed} 条未产出结果（重试耗尽），它们不在下面的明细里 —— 别把"没跑"当成"跑对了"`
}