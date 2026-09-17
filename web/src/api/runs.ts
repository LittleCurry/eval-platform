import { http } from './client'
import type {
    ABReport,
    CaseContextResponse,
    Run,
    RunCaseResult,
    RunJobRef,
    RunProgress,
    RunReport,
} from './types'
import type { PipelinePreviewPayload } from './pipelineProfiles'

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

/**
 * 提交一次评测(M6 闭环的"改配置重跑"用)。
 *
 * 请求体复用配置模板那套字段名(PipelinePreviewPayload): 摊平的 top_k +
 * chunking/generation/judge —— 与服务端提交侧、指纹预览完全同源, 少一处映射就少一次
 * "提交出来的实验跟想的不一样"。未启用的阶段传 null(D14: 不启用就不进快照)。
 */
export function submitRun(payload: PipelinePreviewPayload): Promise<RunJobRef> {
    return http.post('/runs', payload)
}

/** 两次 run 的 A/B 对比(M5-1): 逐题差值 + 显著性 + 翻转题清单 + 分层。 */
export function getRunCompare(left: number, right: number): Promise<ABReport> {
    return http.get(`/compare?left=${left}&right=${right}`)
}