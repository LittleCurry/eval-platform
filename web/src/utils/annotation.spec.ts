import { describe, expect, it } from 'vitest'
import type { Annotation, AnnotationStats, RunCaseResult } from '../api/types'
import {
    joinAnnotations,
    nextStatuses,
    pendingFirst,
    progressText,
    reasonLabel,
    reasonLine,
    reasonTagType,
    shortcutFor,
    statsLine,
    statusLabel,
    statusTagType,
} from './annotation'

function caseRow(caseId: number, qid: string): RunCaseResult {
    return {
        case_id: caseId, qid, question: `问题 ${qid}`,
        retrieved: [], metrics: {}, flags: [],
    }
}

function annotation(caseId: number, status: string, reason = ''): Annotation {
    return {
        id: caseId, project_id: 1, run_id: 155, case_id: caseId,
        status: status as Annotation['status'], reason: reason as Annotation['reason'],
        comment: '', assignee: '', created_by: '', created_at: '', updated_at: '',
    }
}

describe('文案与配色', () => {
    it('状态与归因都有中文', () => {
        expect(statusLabel('open')).toBe('待处理')
        expect(statusLabel('verified')).toBe('已验证')
        expect(statusLabel(undefined)).toBe('未标注')
        expect(reasonLabel('retrieval')).toBe('检索问题')
        expect(reasonLabel('')).toBe('未归类')
    })

    it('未知取值原样返回, 不吞信息', () => {
        expect(reasonLabel('brand_new')).toBe('brand_new')
        expect(statusLabel('whatever')).toBe('whatever')
    })

    it('配色: 已验证绿、待处理黄、幻觉红、检索黄', () => {
        expect(statusTagType('verified')).toBe('success')
        expect(statusTagType('open')).toBe('warning')
        expect(reasonTagType('hallucination')).toBe('error')
        expect(reasonTagType('retrieval')).toBe('warning')
    })
})

describe('nextStatuses(与服务端 canTransition 对应)', () => {
    it('未标注 → 只能先开单', () => {
        expect(nextStatuses(undefined)).toEqual(['open'])
        expect(nextStatuses('')).toEqual(['open'])
    })

    it('主链路 open → fixed → verified, 并允许回退到 open', () => {
        expect(nextStatuses('open')).toEqual(['fixed', 'wontfix'])
        expect(nextStatuses('fixed')).toEqual(['verified', 'open'])
        expect(nextStatuses('verified')).toEqual(['open'])
    })

    it('verified 不能回到 fixed(不许悄悄降级)', () => {
        expect(nextStatuses('verified')).not.toContain('fixed')
    })

    it('未知状态不给按钮(交给服务端报错, 也好过猜)', () => {
        expect(nextStatuses('magic')).toEqual([])
    })
})

describe('shortcutFor', () => {
    it('数字键给归因, 字母键改状态', () => {
        expect(shortcutFor('1')).toEqual({ kind: 'reason', value: 'retrieval' })
        expect(shortcutFor('2')).toEqual({ kind: 'reason', value: 'hallucination' })
        expect(shortcutFor('5')).toEqual({ kind: 'reason', value: 'unknown' })
        expect(shortcutFor('f')).toEqual({ kind: 'status', value: 'fixed' })
        expect(shortcutFor('v')).toEqual({ kind: 'status', value: 'verified' })
    })

    it('n 是下一题, 其它键不拦', () => {
        expect(shortcutFor('n')).toEqual({ kind: 'next', value: '' })
        expect(shortcutFor('x')).toBeNull()
        expect(shortcutFor('Enter')).toBeNull()
    })
})

describe('statsLine / reasonLine', () => {
    const stats: AnnotationStats = {
        total: 12,
        by_status: { open: 3, fixed: 5, verified: 4 },
        by_reason: { retrieval: 6, hallucination: 3, unclassified: 3 },
    }

    it('统计一句话只说非零项', () => {
        expect(statsLine(stats)).toBe('共 12 条 · 待处理 3 · 已修 5 · 已验证 4')
    })

    it('没有标注时说人话', () => {
        expect(statsLine(null)).toBe('还没有任何标注')
        expect(statsLine({ total: 0, by_status: {}, by_reason: {} })).toBe('还没有任何标注')
    })

    it('归因分布把 unclassified 翻成"未归类"', () => {
        expect(reasonLine(stats)).toBe('检索问题 6 · 幻觉 3 · 未归类 3')
    })
})

describe('joinAnnotations', () => {
    it('按 case_id 合并, 没标注的为 null', () => {
        const rows = joinAnnotations(
            [caseRow(1, 'q1'), caseRow(2, 'q2')],
            [annotation(2, 'fixed', 'retrieval')],
        )
        expect(rows[0].annotation).toBeNull()
        expect(rows[1].annotation?.status).toBe('fixed')
    })
})

describe('pendingFirst / progressText', () => {
    it('未标注排最前, 然后待处理 → 已修 → 已验证/不修', () => {
        const rows = joinAnnotations(
            [caseRow(4, 'q4'), caseRow(3, 'q3'), caseRow(2, 'q2'), caseRow(1, 'q1')],
            [
                annotation(1, 'verified'),
                annotation(2, 'open'),
                annotation(3, 'fixed'),
            ],
        )
        const sorted = pendingFirst(rows).map((row) => row.case.qid)
        expect(sorted).toEqual(['q4', 'q2', 'q3', 'q1'])
    })

    it('同组内按 qid 稳定排序', () => {
        const rows = joinAnnotations([caseRow(2, 'zjc-002'), caseRow(1, 'zjc-001')], [])
        expect(pendingFirst(rows).map((row) => row.case.qid)).toEqual(['zjc-001', 'zjc-002'])
    })

    it('进度 = 有标注的题数 / 总题数', () => {
        const rows = joinAnnotations([caseRow(1, 'q1'), caseRow(2, 'q2'), caseRow(3, 'q3')], [annotation(1, 'open')])
        expect(progressText(rows)).toBe('已处理 1/3')
    })
})