import { describe, expect, it } from 'vitest'
import type { CaseContextResponse, JudgePayload, RunMetrics, RunReport } from '../api/types'
import {
    answerSnippet,
    buildReportCsv,
    buildReportMarkdown,
    reportExportFilename,
    attributionText,
    claimCounter,
    contextNotice,
    fallbackContextRows,
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

describe('claimCounter', () => {
    it('给出三种断言的条数(表格与抽屉共用同一口径)', () => {
        expect(claimCounter(judge())).toEqual({ supported: 1, unsupported: 1, irrelevant: 1 })
    })

    it('无判定或无断言时返回 null', () => {
        expect(claimCounter(undefined)).toBeNull()
        expect(claimCounter({ claims: [], rubric: null, meta: {} })).toBeNull()
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

    it('length<=0 表示不截断(表格靠列宽省略 + tooltip 显示全文)', () => {
        const long = '一'.repeat(300)
        expect(answerSnippet(long, 0)).toBe(long)
        expect(answerSnippet('  多   空格  ', 0)).toBe('多 空格')
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
describe('fallbackContextRows', () => {
    it('取不到正文时退化成骨架, found 全为 false', () => {
        const rows = fallbackContextRows([
            { point_id: 'p1', doc_id: 'B02', score: 0.91 },
            { point_id: 'p2', doc_id: 'D01', score: 0.8 },
        ])
        expect(rows).toHaveLength(2)
        expect(rows[0]).toEqual({
            point_id: 'p1', doc_id: 'B02', score: 0.91, section: '', text: '', found: false,
        })
    })

    it('没有 retrieved 时返回空数组, 不抛错', () => {
        expect(fallbackContextRows(undefined)).toEqual([])
        expect(fallbackContextRows([])).toEqual([])
    })
})

describe('contextNotice', () => {
    function response(over: Partial<CaseContextResponse> = {}): CaseContextResponse {
        return {
            collection: 'corpus4_5f45e034',
            chunks: [
                { point_id: 'p1', doc_id: 'B02', text: '正文', found: true },
                { point_id: 'p2', doc_id: 'D01', text: '正文', found: true },
            ],
            ...over,
        }
    }

    it('一切正常时不打扰', () => {
        expect(contextNotice(response())).toBeNull()
        expect(contextNotice(null)).toBeNull()
        expect(contextNotice(undefined)).toBeNull()
    })

    it('后端的降级原因优先展示', () => {
        const notice = contextNotice(response({ error: '向量库未配置, 无法取回 chunk 正文' }))
        expect(notice).toContain('chunk 正文暂不可用')
        expect(notice).toContain('向量库未配置')
    })

    it('部分缺失要说清条数(别被误读成"本来就没有上下文")', () => {
        const notice = contextNotice(response({
            chunks: [
                { point_id: 'p1', found: true, text: 'x' },
                { point_id: 'p2', found: false },
                { point_id: 'p3', found: false },
            ],
        }))
        expect(notice).toContain('2/3 条 chunk 正文缺失')
    })

    it('空 chunk 列表不算异常(该题没有检索结果)', () => {
        expect(contextNotice(response({ chunks: [] }))).toBeNull()
    })
})

// ---- 报告导出(M7-3) ----

function sampleMetrics(): RunMetrics {
    const metrics: RunMetrics = {
        recall_at_k: 0.948611, precision_at_k: 0.2, mrr_at_k: 0.9, hit_at_k: 1,
        hallucination_rate: 0, claim_support_rate: 1, irrelevant_rate: 0,
        cases_judged: 60, cases_total: 60,
    }
    metrics.attribution = {
        version: 1, scope: 'retrieval+judge', k: 5,
        low_rank_limit: 3, low_rank_ratio: 0.5, quality_line: 3,
    }
    return metrics
}

function sampleReport(): RunReport {
    return {
        run: {
            id: 155, project_id: 1, dataset_id: 4, corpus_id: 4, status: 'succeeded',
            config_hash: '449ca546', git_sha: '569d393', metrics: {},
            created_at: '2026-09-17T10:00:00Z', started_at: '2026-09-17T10:00:00Z',
            finished_at: '2026-09-17T10:05:00Z',
        },
        metrics: sampleMetrics(),
        worst_cases: [
            {
                case_id: 61, qid: 'zjc-027', question: '签名失败怎么处理？',
                retrieved: [], metrics: { recall: 0.5, reciprocal_rank: 0.2, hit: 1 },
                flags: ['retrieval_low_rank'],
                answer: '失败后按指数退避重试三次，间隔 5s / 30s / 5min。',
                judge: {
                    claims: [
                        { id: 1, text: '间隔 5s', label: 'supported' },
                        { id: 2, text: '重试 3 次', label: 'unsupported' },
                    ],
                    rubric: null, meta: {},
                },
            },
            {
                case_id: 62, qid: 'zjc-028', question: '导入格式？',
                retrieved: [], metrics: { recall: 0, reciprocal_rank: 0, hit: 0 },
                flags: [], answer: '支持 .xlsx 与 .csv。',
            },
        ],
        flag_counts: { retrieval_low_rank: 1, hallucination: 2 },
    }
}

describe('buildReportCsv', () => {
    it('四段都在: 概况 / 指标 / 归因标签 / 逐题明细', () => {
        const csv = buildReportCsv(sampleReport())
        expect(csv).toContain('run,155')
        expect(csv).toContain('config_hash,449ca546')
        expect(csv).toContain('Recall@k,0.948611')
        expect(csv).toContain('命中但排序靠后,1')
        expect(csv).toContain('zjc-027')
    })

    it('指标名翻译成中文, 陌生 key 原样保留(不吞信息)', () => {
        const csv = buildReportCsv(sampleReport())
        expect(csv).toContain('幻觉率')
        expect(csv).not.toContain('hallucination_rate,')
        const weird = sampleReport()
        weird.metrics = { brand_new_metric: 0.5 }
        expect(buildReportCsv(weird)).toContain('brand_new_metric,0.5')
    })

    it('归因口径(对象)不进指标列表 —— 它是解释信息, 不是指标', () => {
        expect(buildReportCsv(sampleReport())).not.toContain('"version":1')
    })

    it('逐题明细带主因与断言计数, 答案按摘要截断', () => {
        const csv = buildReportCsv(sampleReport())
        expect(csv).toContain('断言(支持/无据/无关)')
        expect(csv).toContain('1/1/0')        // zjc-027: 1 支持 / 1 无据 / 0 无关
        const row = csv.split('\n').find((line) => line.startsWith('zjc-027'))
        expect(row).toBe('zjc-027,命中但排序靠后,1/1/0,0.5,0.2,1,失败后按指数退避重试三次，间隔 5s / 30s / 5min。')
    })

    it('含逗号/引号的答案被正确转义(否则 Excel 里会串列)', () => {
        const report = sampleReport()
        report.worst_cases[0].answer = '顺序是 "先重试, 再降级"'
        const csv = buildReportCsv(report)
        expect(csv).toContain('"顺序是 ""先重试, 再降级"""')
    })

    it('没有判定的题断言计数显示 —, 不假装是 0/0/0', () => {
        const csv = buildReportCsv(sampleReport())
        const lines = csv.split('\n')
        const row = lines.find((line) => line.startsWith('zjc-028'))
        expect(row).toContain('—')
    })
})

describe('buildReportMarkdown', () => {
    it('只放结论级内容: 指标 + 生成判定 + 归因分布', () => {
        const md = buildReportMarkdown(sampleReport(), new Date('2026-09-17T12:00:00Z'))
        expect(md).toContain('# run #155 评测报告')
        expect(md).toContain('| Recall@k | 94.9% |')
        expect(md).toContain('## 生成与判定')
        expect(md).toContain('| 幻觉率 | 0.0% |')
        expect(md).toContain('命中但排序靠后: 1')
        expect(md).toContain('2026-09-17T12:00:00.000Z')
    })

    it('不铺逐题明细(明细留给 CSV), 但要指向它', () => {
        const md = buildReportMarkdown(sampleReport())
        expect(md).not.toContain('zjc-027')
        expect(md).toContain('逐题明细')
    })

    it('只跑检索的 run 不出"生成与判定"段(没有判定就别装作有)', () => {
        const report = sampleReport()
        report.metrics = { recall_at_k: 0.94 } as RunMetrics
        const md = buildReportMarkdown(report)
        expect(md).not.toContain('## 生成与判定')
        expect(md).toContain('| Recall@k | 94.0% |')
    })
})

describe('reportExportFilename', () => {
    it('文件名带 run 与日期, 与 A/B 导出同一套习惯', () => {
        expect(reportExportFilename(155, 'csv', new Date('2026-09-17T12:00:00Z')))
            .toBe('run-155-report-20260917.csv')
    })
})
