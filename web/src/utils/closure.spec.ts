import { describe, expect, it } from 'vitest'
import type { ClosureEvidence, ClosureRecord, ClosureReport, ClosureSummary, Run } from '../api/types'
import {
    candidateRunOptions,
    closureActionText,
    closureCards,
    closureGuard,
    closureSummaryLine,
    closureVerdictLabel,
    closureVerdictType,
    evidenceHighlights,
    evidenceText,
    flagsText,
    formatEvidenceValue,
    verifyHint,
} from './closure'

function summary(overrides: Partial<ClosureSummary> = {}): ClosureSummary {
    return {
        annotated: 3,
        improved: 1,
        stable: 1,
        worsened: 0,
        changed: 1,
        unverifiable: 0,
        by_status: { fixed: 2, open: 1 },
        eligible_for_verify: 1,
        fixed_total: 2,
        ...overrides,
    }
}

function record(overrides: Partial<ClosureRecord> = {}): ClosureRecord {
    return {
        annotation_id: 1, case_id: 61, qid: 'zjc-027', status: 'fixed', reason: 'retrieval',
        comment: '', assignee: '', verdict: 'improved', flags_left: ['retrieval_miss'],
        flags_right: [], evidence: [], eligible_for_verify: true, ...overrides,
    }
}

function report(overrides: Partial<ClosureReport> = {}): ClosureReport {
    return {
        baseline: 112, candidate: 100, comparable: true,
        baseline_hash: '712f6b17', candidate_hash: '4e767020', same_config: false,
        summary: summary(), records: [], notes: [],
        ...overrides,
    }
}

function run(id: number, datasetId: number, hash: string, status = 'succeeded'): Run {
    return {
        id, project_id: 1, dataset_id: datasetId, status, config_hash: hash, git_sha: 'abc',
        metrics: {}, created_at: '',
    }
}

describe('closure verdict', () => {
    it('五种结局都有中文, 未知值原样返回', () => {
        expect(closureVerdictLabel('improved')).toBe('真的变好了')
        expect(closureVerdictLabel('stable')).toBe('没变化')
        expect(closureVerdictLabel('worsened')).toBe('反而变坏')
        expect(closureVerdictLabel('changed')).toBe('换了病因')
        expect(closureVerdictLabel('unverifiable')).toBe('判断不了')
        expect(closureVerdictLabel('weird')).toBe('weird')
    })

    it('变好是绿、变坏是红、换了病因是黄', () => {
        expect(closureVerdictType('improved')).toBe('success')
        expect(closureVerdictType('worsened')).toBe('error')
        expect(closureVerdictType('changed')).toBe('warning')
        expect(closureVerdictType('unverifiable')).toBe('info')
    })
})

describe('flagsText', () => {
    it('标签翻译成人话, 空列表说"干净"', () => {
        expect(flagsText(['retrieval_miss'])).toContain('检索')
        expect(flagsText([])).toBe('干净')
        expect(flagsText(null)).toBe('干净')
    })
})

describe('closureSummaryLine / closureActionText', () => {
    it('有可销单的题时给出销单提示', () => {
        const line = closureSummaryLine(summary())
        expect(line).toContain('已标 3 题')
        expect(line).toContain('fixed 2 题, 1 题确认变好可销单')
        expect(line).toContain('1 题换了病因')
        expect(closureActionText(summary())).toContain('一键推进到 verified')
    })

    it('没有可销单但有变坏时, 先查回归', () => {
        const text = closureActionText(summary({ eligible_for_verify: 0, worsened: 2 }))
        expect(text).toContain('回归')
    })

    it('没标注时说清下一步去哪', () => {
        expect(closureActionText(summary({ annotated: 0 }))).toContain('标注工作台')
        expect(closureSummaryLine(summary({ annotated: 0 }))).toContain('还没有可核对的标注')
    })

    it('没有任何动作项时不硬凑建议', () => {
        const text = closureActionText(summary({ eligible_for_verify: 0, worsened: 0 }))
        expect(text).toContain('暂时没有可销单的题')
    })
})

describe('evidence formatting', () => {
    it('比例按百分比, 分数按小数', () => {
        expect(formatEvidenceValue(0)).toBe('0%')
        expect(formatEvidenceValue(0.5)).toBe('50%')
        expect(formatEvidenceValue(1)).toBe('100%')
        expect(formatEvidenceValue(3.5)).toBe('3.50')
        expect(formatEvidenceValue(Number.NaN)).toBe('—')
    })

    it('不可比的证据不给数字 —— "没判定"不能被读成 0 分', () => {
        const text = evidenceText({
            metric: 'hallucination_rate', left: 0, right: 0, delta: 0,
            higher_is_better: false, direction: 'same', comparable: false,
        }, (metric) => (metric === 'hallucination_rate' ? '幻觉率' : metric))
        expect(text).toBe('幻觉率 不可比')
    })

    it('证据文字带前后值', () => {
        const text = evidenceText({
            metric: 'recall', left: 0, right: 1, delta: 1,
            higher_is_better: true, direction: 'better', comparable: true,
        }, () => 'Recall@k')
        expect(text).toBe('Recall@k 0% → 100%')
    })
})

describe('evidenceHighlights', () => {
    const evidence: ClosureEvidence[] = [
        { metric: 'precision', left: 0.2, right: 0.2, delta: 0, higher_is_better: true, direction: 'same', comparable: true },
        { metric: 'recall', left: 0, right: 1, delta: 1, higher_is_better: true, direction: 'better', comparable: true },
        { metric: 'hallucination_rate', left: 0, right: 0.5, delta: 0.5, higher_is_better: false, direction: 'worse', comparable: true },
        { metric: 'helpfulness', left: 0, right: 0, delta: 0, higher_is_better: true, direction: 'same', comparable: false },
    ]

    it('变坏的排最前(最该看的先露出来), 不可比的排最后', () => {
        const picked = evidenceHighlights(evidence, 4)
        expect(picked.map((item) => item.metric)).toEqual([
            'hallucination_rate', 'recall', 'precision', 'helpfulness',
        ])
    })

    it('只取 limit 条', () => {
        expect(evidenceHighlights(evidence, 2).map((item) => item.metric))
            .toEqual(['hallucination_rate', 'recall'])
    })

    it('全部落在噪声里时保持原顺序, 不假装有变化', () => {
        const flat: ClosureEvidence[] = [
            { metric: 'recall', left: 1, right: 1, delta: 0, higher_is_better: true, direction: 'same', comparable: true },
            { metric: 'precision', left: 0.2, right: 0.2, delta: 0, higher_is_better: true, direction: 'same', comparable: true },
        ]
        expect(evidenceHighlights(flat, 2).map((item) => item.metric)).toEqual(['recall', 'precision'])
    })

    it('没有证据时返回空数组', () => {
        expect(evidenceHighlights([], 3)).toEqual([])
    })
})

describe('verifyHint', () => {
    it('可销单时说可以销单', () => {
        const hint = verifyHint(record())
        expect(hint.ok).toBe(true)
        expect(hint.text).toBe('可以销单')
    })

    it('不能销单时把服务端给的原因原样带出来', () => {
        const hint = verifyHint(record({
            eligible_for_verify: false,
            verdict: 'stable',
            blocked_reason: '仍然有同样的标签: retrieval_miss',
        }))
        expect(hint.ok).toBe(false)
        expect(hint.text).toContain('retrieval_miss')
        expect(hint.type).toBe('default')
    })

    it('服务端没给原因时也不留空', () => {
        expect(verifyHint(record({ eligible_for_verify: false, blocked_reason: undefined })).text)
            .toBe('还不能销单')
    })
})

describe('closureGuard', () => {
    it('不可比优先报(这是最致命的一条)', () => {
        const guard = closureGuard(report({ comparable: false, reason: '跨评测集' }))
        expect(guard?.type).toBe('error')
        expect(guard?.text).toContain('不成立')
    })

    it('归因缺失时报错并给出补救指令', () => {
        const guard = closureGuard(report({ attribution_missing: '右侧 run 未做过归因' }))
        expect(guard?.type).toBe('error')
        expect(guard?.text).toContain('未做过归因')
    })

    it('同配置时报"这是复现不是实验"', () => {
        const guard = closureGuard(report({ same_config: true }))
        expect(guard?.type).toBe('warning')
        expect(guard?.text).toContain('复现')
    })

    it('一切正常时不打扰', () => {
        expect(closureGuard(report())).toBeNull()
    })
})

describe('closureCards', () => {
    it('四张卡固定顺序, 每条都带一句解释', () => {
        const cards = closureCards(summary({ eligible_for_verify: 3, worsened: 1, changed: 2, unverifiable: 4 }))
        expect(cards.map((card) => card.label)).toEqual(['可销单', '反而变坏', '换了病因', '判断不了'])
        expect(cards.map((card) => card.value)).toEqual([3, 1, 2, 4])
        expect(cards[0].hint).toContain('fixed')
    })
})

describe('candidateRunOptions', () => {
    const baseline = run(112, 4, '712f6b17')
    const runs = [
        baseline,
        run(100, 4, '4e767020'),
        run(101, 4, '4e767020'),
        run(3, 3, 'b6598ed9'),
        run(200, 4, 'aaaa1111', 'running'),
    ]

    it('排除自己与未完成的 run, 同集排前面', () => {
        const options = candidateRunOptions(runs, baseline, 112)
        expect(options.map((option) => option.value)).toEqual([100, 101, 3])
    })

    it('同配置的候选要标"复现" —— 拿它做闭环等于测噪声', () => {
        // 把基线换成 #100(4e767020): 此时 #101 与它同配置, 必须被标出来
        const sameAsBaseline = candidateRunOptions(runs, run(100, 4, '4e767020'), 100)
        expect(sameAsBaseline.find((option) => option.value === 101)?.label)
            .toBe('#101 · 同集 · 同配置=复现')
        expect(sameAsBaseline.find((option) => option.value === 112)?.label).toBe('#112 · 同集')
    })

    it('跨集的候选标出 dataset, 让人知道会被判不可比', () => {
        const options = candidateRunOptions(runs, baseline, 112)
        expect(options.find((option) => option.value === 3)?.label).toBe('#3 · dataset 3')
    })

    it('没有基线信息时全部按跨集处理(不假装知道)', () => {
        const options = candidateRunOptions(runs, null, 112)
        expect(options.every((option) => option.label.includes('dataset'))).toBe(true)
    })
})
