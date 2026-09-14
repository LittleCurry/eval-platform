import { describe, expect, it } from 'vitest'
import type { JudgePayload, RunMetrics } from '../api/types'
import {
    answerSnippet,
    attributionText,
    claimLabel,
    claimSummary,
    claimTagType,
    flagLabel,
    flagTagType,
    generationSummary,
    hasLlmMetrics,
    missingCaseNotice,
    primaryFlag,
    ratesConsistent,
    ratesSum,
    rubricSummary,
} from './report'

function judge(over: Partial<JudgePayload> = {}): JudgePayload {
    return {
        claims: [
            { id: 1, text: '断言一', label: 'supported', evidence: '资料原文' },
            { id: 2, text: '断言二', label: 'unsupported' },
            { id: 3, text: '断言三', label: 'irrelevant' },
        ],
        rubric: { relevance: 5, helpfulness: 4, reason: '覆盖完整' },
        meta: {},
        ...over,
    }
}

describe('flagLabel', () => {
    it('已知标签给中文解释(不是一串英文)', () => {
        expect(flagLabel('retrieval_miss')).toBe('检索漏召回(全未命中)')
        expect(flagLabel('hallucination')).toBe('幻觉(有断言无据)')
    })

    it('未知标签原样返回, 不吞掉信息', () => {
        expect(flagLabel('brand_new_flag')).toBe('brand_new_flag')
    })
})

describe('primaryFlag', () => {
    it('取第一位(worker 已按优先级排序)', () => {
        expect(primaryFlag(['anchor_incomplete', 'retrieval_miss'])).toBe('anchor_incomplete')
    })

    it('空数组与 undefined 都是 null', () => {
        expect(primaryFlag([])).toBeNull()
        expect(primaryFlag(undefined)).toBeNull()
    })
})

describe('flagTagType', () => {
    it('幻觉与不可评测是红, 检索类是黄, 质量类是蓝', () => {
        expect(flagTagType('hallucination')).toBe('error')
        expect(flagTagType('no_gold')).toBe('error')
        expect(flagTagType('retrieval_partial')).toBe('warning')
        expect(flagTagType('generation_quality')).toBe('info')
        expect(flagTagType(null)).toBe('default')
    })
})

describe('claimSummary', () => {
    it('按三种标签计数', () => {
        expect(claimSummary(judge())).toBe('1 支持 / 1 无据 / 1 无关')
    })

    it('无判定或无断言时返回 null(不能显示成 0 支持)', () => {
        expect(claimSummary(undefined)).toBeNull()
        expect(claimSummary({ claims: [], rubric: null, meta: {} })).toBeNull()
    })
})

describe('rubricSummary', () => {
    it('展示 R/H 两项', () => {
        expect(rubricSummary(judge())).toBe('R5 H4')
    })

    it('未启用 rubric 时返回 null, 不能当成 0 分', () => {
        expect(rubricSummary(judge({ rubric: null }))).toBeNull()
        expect(rubricSummary(undefined)).toBeNull()
    })
})

describe('claimLabel / claimTagType', () => {
    it('三种标签都有中文与配色', () => {
        expect(claimLabel('supported')).toBe('有据')
        expect(claimLabel('unsupported')).toBe('无据')
        expect(claimLabel('irrelevant')).toBe('无关')
        expect(claimTagType('unsupported')).toBe('error')
        expect(claimTagType('supported')).toBe('success')
        expect(claimTagType('irrelevant')).toBe('warning')
    })
})

describe('answerSnippet', () => {
    it('压平空白并按长度截断', () => {
        expect(answerSnippet('  线索  超过 7 天  ')).toBe('线索 超过 7 天')
        expect(answerSnippet('一'.repeat(50), 10)).toBe(`${'一'.repeat(10)}…`)
    })

    it('空答案与空白答案返回 null', () => {
        expect(answerSnippet(undefined)).toBeNull()
        expect(answerSnippet('   ')).toBeNull()
    })
})

describe('generationSummary', () => {
    it('拼出模型与用量', () => {
        expect(generationSummary({
            model: 'deepseek-ai/DeepSeek-V3.2', prompt_tokens: 634, completion_tokens: 24, latency_ms: 3163,
        })).toBe('deepseek-ai/DeepSeek-V3.2 · 634+24 tokens · 3.2s')
    })

    it('没有生成元信息时返回 null', () => {
        expect(generationSummary(undefined)).toBeNull()
        expect(generationSummary({ prompt_tokens: 10 })).toBeNull()
    })
})

describe('hasLlmMetrics', () => {
    it('只跑检索的 run 不展示生成/判定卡', () => {
        expect(hasLlmMetrics({ recall_at_k: 0.94, k: 5 } as RunMetrics)).toBe(false)
        expect(hasLlmMetrics(undefined)).toBe(false)
    })

    it('有生成或判定痕迹就展示', () => {
        expect(hasLlmMetrics({ answers_generated: 60 } as RunMetrics)).toBe(true)
        expect(hasLlmMetrics({ cases_judged: 60 } as RunMetrics)).toBe(true)
    })
})

describe('ratesSum / ratesConsistent', () => {
    it('三率之和为 1 时自洽', () => {
        const metrics = {
            claims_total: 144,
            claim_support_rate: 1,
            hallucination_rate: 0,
            irrelevant_rate: 0,
        } as RunMetrics
        expect(ratesSum(metrics)).toBe(1)
        expect(ratesConsistent(metrics)).toBe(true)
    })

    it('容差覆盖"三项各自四舍五入到 6 位"的误差', () => {
        const metrics = {
            claims_total: 3,
            claim_support_rate: 0.333333,
            hallucination_rate: 0.333334,
            irrelevant_rate: 0.333333,
        } as RunMetrics
        expect(ratesConsistent(metrics)).toBe(true)
    })

    it('真的不自洽时判 false', () => {
        const metrics = {
            claims_total: 10,
            claim_support_rate: 0.6,
            hallucination_rate: 0.2,
            irrelevant_rate: 0.1,
        } as RunMetrics
        expect(ratesConsistent(metrics)).toBe(false)
    })

    it('没有断言时返回 null(0/0 不是自洽, 是无从判断)', () => {
        expect(ratesSum({ claims_total: 0 } as RunMetrics)).toBeNull()
        expect(ratesConsistent({} as RunMetrics)).toBeNull()
    })
})

describe('attributionText', () => {
    it('把规则版本与阈值写成一行', () => {
        const metrics = {
            attribution: {
                version: 1, scope: 'retrieval+judge', k: 5,
                low_rank_limit: 3, low_rank_ratio: 0.5, quality_line: 3,
            },
        } as RunMetrics
        expect(attributionText(metrics)).toBe(
            '归因口径：规则 v1 · k=5 · 排序阈值 >3 · 达标线 ≤3 · 覆盖 retrieval+judge',
        )
    })

    it('历史 run 没有归因元信息时返回 null', () => {
        expect(attributionText({ recall_at_k: 0.9 } as RunMetrics)).toBeNull()
        expect(attributionText(undefined)).toBeNull()
    })
})

describe('missingCaseNotice', () => {
    it('有失败条目时必须提示', () => {
        expect(missingCaseNotice(3)).toContain('3 条未产出结果')
    })

    it('没有失败条目时不打扰', () => {
        expect(missingCaseNotice(0)).toBeNull()
        expect(missingCaseNotice(undefined)).toBeNull()
    })
})