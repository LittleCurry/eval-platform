import { http } from './client'
import type { Annotation, AnnotationStats, AnnotationSuggestion } from './types'

/** 打标/改状态的请求体: 字段都可选 —— 没传 = 不改, 显式空串 = 清空(与服务端约定一致)。 */
export interface AnnotationUpsertPayload {
    run_id: number
    case_id: number
    status?: string
    reason?: string
    comment?: string
    assignee?: string
    project_id?: number
}

export function listAnnotations(options: {
    runId?: number
    caseId?: number
    status?: string
    limit?: number
} = {}): Promise<Annotation[]> {
    const query = new URLSearchParams()
    if (options.runId) query.set('run_id', String(options.runId))
    if (options.caseId) query.set('case_id', String(options.caseId))
    if (options.status) query.set('status', options.status)
    if (options.limit) query.set('limit', String(options.limit))
    const suffix = query.toString() ? `?${query.toString()}` : ''
    return http.get(`/annotations${suffix}`)
}

/**
 * 按 (run, case) 打标: 已有标注则是更新(服务端会校验状态流转)。
 * 这样工作台就是"点一下打标", 不必先查再决定 POST 还是 PATCH。
 */
export function upsertAnnotation(payload: AnnotationUpsertPayload): Promise<Annotation> {
    return http.post('/annotations', payload)
}

export function updateAnnotation(
    id: number,
    payload: { status?: string; reason?: string; comment?: string; assignee?: string },
): Promise<Annotation> {
    return http.patch(`/annotations/${id}`, payload)
}

export function deleteAnnotation(id: number): Promise<void> {
    return http.del(`/annotations/${id}`)
}

export function getAnnotationStats(runId: number): Promise<AnnotationStats> {
    return http.get(`/annotation-stats?run_id=${runId}`)
}

export function getAnnotationSuggestion(runId: number, caseId: number): Promise<AnnotationSuggestion> {
    return http.get(`/annotation-suggestion?run_id=${runId}&case_id=${caseId}`)
}