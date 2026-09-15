import { http } from './client'
import type { CaseContextResponse, Run, RunCaseResult, RunProgress, RunReport } from './types'

export interface ListRunsParams {
    datasetId?: number
    projectId?: number
    limit?: number
}

export function listRuns(params: ListRunsParams = {}): Promise<Run[]> {
    const query = new URLSearchParams()
    if (params.datasetId) query.set('dataset_id', String(params.datasetId))
    if (params.projectId) query.set('project_id', String(params.projectId))
    if (params.limit) query.set('limit', String(params.limit))
    const suffix = query.toString() ? `?${query.toString()}` : ''
    return http.get(`/runs${suffix}`)
}

export function getRun(id: number): Promise<Run> {
    return http.get(`/runs/${id}`)
}

export function getRunCaseResults(
    id: number,
    options: { limit?: number; flaggedOnly?: boolean } = {},
): Promise<RunCaseResult[]> {
    const query = new URLSearchParams()
    if (options.limit) query.set('limit', String(options.limit))
    if (options.flaggedOnly) query.set('flagged', '1')
    const suffix = query.toString() ? `?${query.toString()}` : ''
    return http.get(`/runs/${id}/case-results${suffix}`)
}

export function getRunReport(id: number, worst = 10): Promise<RunReport> {
    return http.get(`/runs/${id}/report?worst=${worst}`)
}

/** 任务进度(含"疑似 worker 掉线"标记), 供进度条与自动刷新使用。 */
export function getRunProgress(id: number): Promise<RunProgress> {
    return http.get(`/runs/${id}/progress`)
}

/**
 * 某题检索到的 chunk 正文(M4-4.1)。
 * 正文只在向量库里有一份、不落库(process.md D17), 所以抽屉打开时才按需取;
 * 向量库不可用时后端仍返回 200 + error 字段, 由调用方降级展示。
 */
export function getCaseContext(runId: number, caseId: number): Promise<CaseContextResponse> {
    return http.get(`/runs/${runId}/cases/${caseId}/context`)
}