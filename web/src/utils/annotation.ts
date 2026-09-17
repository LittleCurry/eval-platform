// 标注工作台的纯逻辑(M6-2): 文案、配色、状态机镜像、快捷键、待办排序。
//
// 为什么"状态机"在这里也有一份: 前端需要它来**决定显示哪些按钮**。但权威在服务端
// (canTransition 会拒绝非法流转), 前端这份只是视图, 不承担校验责任 ——
// 两边不一致时结果是"按钮点了报 409", 而不是"状态被悄悄改坏"。

import type { Annotation, AnnotationStats, AnnotationStatus, RunCaseResult } from '../api/types'
import type { TagType } from './format'

export const ANNOTATION_STATUSES: AnnotationStatus[] = ['open', 'fixed', 'verified', 'wontfix']

export const STATUS_LABELS: Record<string, string> = {
    open: '待处理',
    fixed: '已修',
    verified: '已验证',
    wontfix: '不修',
}

export function statusLabel(status?: string): string {
    if (!status) return '未标注'
    return STATUS_LABELS[status] ?? status
}

export function statusTagType(status?: string): TagType {
    switch (status) {
        case 'open':
            return 'warning'
        case 'fixed':
            return 'info'
        case 'verified':
            return 'success'
        case 'wontfix':
            return 'default'
        default:
            return 'default'
    }
}

/** 归因枚举与 worker 的标签族对齐(见 Go 侧 reasonFamily)。 */
export const REASONS = ['retrieval', 'hallucination', 'generation', 'dataset', 'unknown'] as const

export const REASON_LABELS: Record<string, string> = {
    retrieval: '检索问题',
    hallucination: '幻觉',
    generation: '生成质量',
    dataset: '数据集/标注',
    unknown: '说不清',
}

export function reasonLabel(reason?: string): string {
    if (!reason) return '未归类'
    return REASON_LABELS[reason] ?? reason
}

export function reasonTagType(reason?: string): TagType {
    switch (reason) {
        case 'hallucination':
            return 'error'
        case 'retrieval':
            return 'warning'
        case 'generation':
            return 'info'
        case 'dataset':
            return 'default'
        default:
            return 'default'
    }
}

/**
 * 当前状态能流转到哪些状态(与服务端 canTransition 对应)。
 * 注意 verified 只能回到 open: 不允许"已验证 → 已修"这种悄悄降级。
 */
export function nextStatuses(current?: string): AnnotationStatus[] {
    switch (current) {
        case undefined:
        case '':
            return ['open']
        case 'open':
            return ['fixed', 'wontfix']
        case 'fixed':
            return ['verified', 'open']
        case 'verified':
            return ['open']
        case 'wontfix':
            return ['open']
        default:
            return []
    }
}

/** 快捷键: 数字打归因, 字母改状态, n = 下一题。返回 null 表示不是本工作台的快捷键。 */
export function shortcutFor(key: string): { kind: 'reason' | 'status' | 'next'; value: string } | null {
    const reasonKeys: Record<string, string> = {
        '1': 'retrieval',
        '2': 'hallucination',
        '3': 'generation',
        '4': 'dataset',
        '5': 'unknown',
    }
    const statusKeys: Record<string, string> = {
        o: 'open',
        f: 'fixed',
        v: 'verified',
        w: 'wontfix',
    }
    if (reasonKeys[key]) return { kind: 'reason', value: reasonKeys[key] }
    if (statusKeys[key]) return { kind: 'status', value: statusKeys[key] }
    if (key === 'n') return { kind: 'next', value: '' }
    return null
}

export const SHORTCUT_HELP =
    '快捷键：1 检索问题 · 2 幻觉 · 3 生成质量 · 4 数据集/标注 · 5 说不清 ｜ o 待处理 · f 已修 · v 已验证 · w 不修 ｜ n 下一题'

/** 统计一句话: "共 12 条 · 待处理 3 · 已修 5 · 已验证 4"。 */
export function statsLine(stats?: AnnotationStats | null): string {
    if (!stats || stats.total === 0) return '还没有任何标注'
    const parts = ANNOTATION_STATUSES
        .filter((status) => (stats.by_status?.[status] ?? 0) > 0)
        .map((status) => `${statusLabel(status)} ${stats.by_status[status]}`)
    return `共 ${stats.total} 条 · ${parts.join(' · ')}`
}

/** 归因分布一句话: "检索问题 6 · 幻觉 3"（空的归因不显示）。 */
export function reasonLine(stats?: AnnotationStats | null): string {
    if (!stats) return ''
    const parts = Object.entries(stats.by_reason ?? {})
        .filter(([, count]) => count > 0)
        .map(([reason, count]) => `${reasonLabel(reason === 'unclassified' ? '' : reason)} ${count}`)
    return parts.join(' · ')
}

export interface AnnotationRow {
    case: RunCaseResult
    annotation: Annotation | null
}

/** 把单题结果与已有标注按 case_id 合并(工作台的主数据)。 */
export function joinAnnotations(
    cases: RunCaseResult[],
    annotations: Annotation[],
): AnnotationRow[] {
    const byCase = new Map<number, Annotation>()
    for (const item of annotations) byCase.set(item.case_id, item)
    return cases.map((item) => ({ case: item, annotation: byCase.get(item.case_id) ?? null }))
}

/**
 * 待办优先排序: 未标注 → 待处理 → 已修 → 已验证/不修, 同组内按 qid。
 * 工作台的第一屏应该是"还没人看过的题", 而不是按 case_id 排出来的随机顺序。
 */
export function pendingFirst(rows: AnnotationRow[]): AnnotationRow[] {
    const rank = (row: AnnotationRow): number => {
        const status = row.annotation?.status
        switch (status) {
            case undefined:
                return 0
            case 'open':
                return 1
            case 'fixed':
                return 2
            case 'verified':
                return 3
            case 'wontfix':
                return 4
            default:
                return 5
        }
    }
    return [...rows].sort((a, b) => {
        const diff = rank(a) - rank(b)
        if (diff !== 0) return diff
        return a.case.qid.localeCompare(b.case.qid)
    })
}

/** 进度: "已处理 8/25"（"处理过" = 有标注, 不论状态）。 */
export function progressText(rows: AnnotationRow[]): string {
    const done = rows.filter((row) => row.annotation !== null).length
    return `已处理 ${done}/${rows.length}`
}