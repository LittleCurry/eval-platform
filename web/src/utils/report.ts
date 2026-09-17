// 报告页的展示口径(纯函数, 便于单测; 测试环境是 node, 所以这里不碰 DOM)。
//
// 中文标签与 worker 侧 FLAG_ZH 一一对应 —— 唯一来源是 worker/app/eval/attribution.py。
// 两边各译一套就会出现"报告说幻觉、抽屉说无据"的错位, 所以改动那边时这里要同步。

import type {
    CaseContextResponse,
    GenerationPayload,
    JudgePayload,
    RetrievedItem,
    RunCaseResult,
    RunMetrics,
    RunReport,
} from '../api/types'
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

/**
 * 三种断言的条数; 没有判定或没有断言时返回 null(调用方显示占位符)。
 * 表格用紧凑计数、抽屉用长文案, 都从这里取数, 保证两处口径一致。
 */
export function claimCounter(judge?: JudgePayload | null): {
    supported: number
    unsupported: number
    irrelevant: number
} | null {
    const claims = judge?.claims
    if (!claims || claims.length === 0) return null
    const count = (label: string) => claims.filter((claim) => claim.label === label).length
    return {
        supported: count('supported'),
        unsupported: count('unsupported'),
        irrelevant: count('irrelevant'),
    }
}

/** 断言计数文案(抽屉用); 没有判定或没有断言时返回 null。 */
export function claimSummary(judge?: JudgePayload | null): string | null {
    const counts = claimCounter(judge)
    if (counts === null) return null
    return `${counts.supported} 支持 / ${counts.unsupported} 无据 / ${counts.irrelevant} 无关`
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

/**
 * 答案单行化; 空白答案返回 null, 不显示空壳。
 * length <= 0 表示不截断 —— 表格列自己会做省略并在悬浮时显示全文,
 * 这里再截一刀只会让 tooltip 也只剩半句。
 */
export function answerSnippet(answer?: string, length = 40): string | null {
    const text = (answer ?? '').replace(/\s+/g, ' ').trim()
    if (!text) return null
    if (length <= 0) return text
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

// ---- 检索上下文(M4-4.1) ----

/** 抽屉里"检索上下文"的一行: 正文可能来自向量库, 也可能是本地兜底的骨架。 */
export interface ContextRow {
    point_id: string
    doc_id: string
    score?: number
    section: string
    text: string
    found: boolean
}

/**
 * 取不到正文时的兜底(旧后端/降级/断网): 只列 retrieved 的骨架。
 * 至少让同事看到"这道题检索了哪几个 chunk", 而不是一片空白。
 */
export function fallbackContextRows(retrieved?: RetrievedItem[]): ContextRow[] {
    if (!retrieved || retrieved.length === 0) return []
    return retrieved.map((item) => ({
        point_id: item.point_id,
        doc_id: item.doc_id ?? '',
        score: item.score,
        section: '',
        text: '',
        found: false,
    }))
}

/**
 * 上下文面板的提示: 后端给的降级原因优先, 其次是"部分正文缺失"的统计。
 * 一切正常返回 null(不打扰)。found=false 的条数要单独说清楚 ——
 * 否则"集合被重建"会被误读成"这道题本来就没有上下文"。
 */
export function contextNotice(response?: CaseContextResponse | null): string | null {
    if (!response) return null
    if (response.error) return `chunk 正文暂不可用：${response.error}`
    const chunks = response.chunks ?? []
    if (chunks.length === 0) return null
    const missing = chunks.filter((chunk) => !chunk.found).length
    if (missing > 0) {
        return `${missing}/${chunks.length} 条 chunk 正文缺失（该 point 不在当前集合里，索引可能被重建过）`
    }
    return null
}

// ---- 报告导出(M7-3) ----
//
// 为什么导出放前端而不是服务端: 报告接口已经返回结构化结果, CSV/Markdown 只是它的视图;
// 服务端再实现一遍等于给自己埋一个"两处口径不一致"的隐患(与 D19 同一条理由)。
// 这里复用 A/B 对比页那套 CSV 转义约定, 避免同一个仓库里出现两种 CSV 风格。

function csvCell(value: unknown): string {
    const text = value === undefined || value === null ? '' : String(value)
    if (/[",\n\r]/.test(text)) return `"${text.replace(/"/g, '""')}"`
    return text
}

function csvRows(rows: unknown[][]): string {
    return rows.map((row) => row.map(csvCell).join(',')).join('\n')
}

/** run 级指标的中文名(与报告页卡片一致, 免得导出文件里全是英文 key)。 */
const METRIC_LABELS: Record<string, string> = {
    recall_at_k: 'Recall@k',
    precision_at_k: 'Precision@k',
    mrr_at_k: 'MRR@k',
    hit_at_k: 'Hit@k',
    avg_hits: '平均命中数',
    avg_gold_chunks: '平均 gold 数',
    cases_total: '题目总数',
    cases_evaluated: '参与评测题数',
    cases_skipped_no_gold: '因缺 gold 跳过',
    answers_generated: '生成答案题数',
    cases_judged: '判定题数',
    claim_support_rate: '断言支持率',
    claim_support_rate_raw: '(未舍入)断言支持率',
    hallucination_rate: '幻觉率',
    irrelevant_rate: '无关断言率',
    claims_total: '断言总数',
    claims_supported: '有据断言数',
    claims_unsupported: '无据断言数',
    claims_irrelevant: '无关断言数',
    avg_claims_per_answer: '平均断言数',
    relevance_avg: 'relevance 均值',
    helpfulness_avg: 'helpfulness 均值',
    rubric_cases: '打分题数',
    judge_cache_hits: 'judge 缓存命中',
    judge_prompt_tokens: 'judge prompt tokens',
    judge_completion_tokens: 'judge completion tokens',
    prompt_tokens: '生成 prompt tokens',
    completion_tokens: '生成 completion tokens',
}

export function metricLabel(name: string): string {
    return METRIC_LABELS[name] ?? name
}

/**
 * 导出 CSV: 四段(概况 / 指标 / 归因标签 / 逐题明细)。
 *
 * 逐题明细里带上"主因 + 断言计数 + 答案摘要"——同事拿这个文件就能自己核对,
 * 不必再回来点开页面; 答案正文按摘要截断(完整答案在页面抽屉里看)。
 */
export function buildReportCsv(report: RunReport): string {
    const run = report.run
    const metrics = report.metrics ?? {}
    const blocks: string[] = []

    blocks.push(csvRows([
        ['run', run.id],
        ['评测集', run.dataset_id],
        ['状态', run.status],
        ['config_hash', run.config_hash],
        ['git_sha', run.git_sha],
        ['开始', run.started_at ?? ''],
        ['结束', run.finished_at ?? ''],
    ]))

    blocks.push(csvRows([
        ['指标', '值'],
        ...Object.keys(metrics)
            .filter((key) => !key.startsWith('attribution'))
            .sort()
            .map((key) => [metricLabel(key), typeof metrics[key] === 'object' ? JSON.stringify(metrics[key]) : metrics[key]]),
    ]))

    const flagCounts = Object.entries(report.flag_counts ?? {}).sort((a, b) => b[1] - a[1])
    if (flagCounts.length > 0) {
        blocks.push(csvRows([
            ['归因标签', '题数'],
            ...flagCounts.map(([flag, count]) => [flagLabel(flag), count]),
        ]))
    }

    blocks.push(csvRows([
        ['qid', '主因', '断言(支持/无据/无关)', 'recall', 'MRR', '命中', '答案摘要'],
        ...(report.worst_cases ?? []).map((row) => [
            row.qid,
            flagLabel(primaryFlag(row.flags) ?? '') || '—',
            claimCounterText(row),
            row.metrics?.recall ?? '',
            row.metrics?.reciprocal_rank ?? '',
            row.metrics?.hit ?? '',
            answerSnippet(row.answer, 60) ?? '',
        ]),
    ]))

    return blocks.join('\n\n')
}

function claimCounterText(row: RunCaseResult): string {
    // claimCounter 在"没有判定/断言为空"时返回 null —— 那时显示 — 而不是 0/0/0,
    // 否则导出文件会把"没判定"读成"零断言"
    const counter = claimCounter(row.judge)
    if (!counter) return '—'
    return `${counter.supported}/${counter.unsupported}/${counter.irrelevant}`
}

/**
 * 导出 Markdown: 给人看的版本(贴到文档/群里)。
 * 与 CSV 的区别不是格式, 而是**取舍**: 这里只放"结论级"内容(指标卡 + 主因分布),
 * 不铺逐题明细 —— 明细留给 CSV。
 */
export function buildReportMarkdown(report: RunReport, now = new Date()): string {
    const run = report.run
    const metrics = report.metrics ?? {}
    const lines: string[] = []

    lines.push(`# run #${run.id} 评测报告`)
    lines.push('')
    lines.push(`- 评测集: dataset ${run.dataset_id}${run.corpus_id ? ` · corpus ${run.corpus_id}` : ''}`)
    lines.push(`- 状态: ${run.status}`)
    lines.push(`- config_hash: \`${run.config_hash}\` · git_sha: \`${run.git_sha}\``)
    lines.push(`- 生成时间: ${now.toISOString()}`)
    lines.push('')

    const rate = (value: unknown) =>
        typeof value === 'number' ? `${(value * 100).toFixed(1)}%` : '—'
    lines.push('## 指标')
    lines.push('')
    lines.push('| 指标 | 值 |')
    lines.push('| --- | --- |')
    for (const key of ['recall_at_k', 'precision_at_k', 'mrr_at_k', 'hit_at_k']) {
        if (metrics[key] !== undefined) lines.push(`| ${metricLabel(key)} | ${rate(metrics[key])} |`)
    }
    if (hasLlmMetrics(metrics)) {
        lines.push('')
        lines.push('## 生成与判定')
        lines.push('')
        lines.push('| 指标 | 值 |')
        lines.push('| --- | --- |')
        lines.push(`| 幻觉率 | ${rate(metrics.hallucination_rate)} |`)
        lines.push(`| 断言支持率 | ${rate(metrics.claim_support_rate)} |`)
        lines.push(`| 无关断言率 | ${rate(metrics.irrelevant_rate)} |`)
        lines.push(`| 判定题数 | ${metrics.cases_judged ?? '—'} |`)
    }

    const attribution = attributionText(metrics)
    if (attribution) {
        lines.push('')
        lines.push(`> 归因口径: ${attribution}`)
    }

    const flagCounts = Object.entries(report.flag_counts ?? {}).sort((a, b) => b[1] - a[1])
    if (flagCounts.length > 0) {
        lines.push('')
        lines.push('## 归因标签分布')
        lines.push('')
        for (const [flag, count] of flagCounts) {
            lines.push(`- ${flagLabel(flag)}: ${count}`)
        }
    }

    lines.push('')
    lines.push(`> 逐题明细（含每题的断言与答案摘要）见同目录的 CSV 导出。`)
    return lines.join('\n')
}

/** 导出文件名: 与 A/B 对比页同一套命名习惯。 */
export function reportExportFilename(runId: number, ext: string, now = new Date()): string {
    const pad = (value: number) => String(value).padStart(2, '0')
    const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}`
    return `run-${runId}-report-${stamp}.${ext}`
}
