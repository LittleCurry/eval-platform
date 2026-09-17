import { http } from './client'
import type {
    PipelineProfile,
    PipelineProfileConfig,
    PipelinePreview,
    ProfileChunking,
    ProfileGeneration,
    ProfileJudge,
} from './types'

/**
 * 提交前预览用的请求体。
 *
 * 注意它**不是**嵌套的 config: 服务端这个端点直接吃提交侧的字段名
 * (chunking/top_k/generation/judge) + corpus_id/dataset_id —— 指纹里含数据来源(D7),
 * 少了后两项算出来的 hash 没有意义。
 */
export interface PipelinePreviewPayload {
    corpus_id: number
    dataset_id: number
    chunking?: ProfileChunking
    top_k?: number
    generation?: ProfileGeneration | null
    judge?: ProfileJudge | null
}

export function listPipelineProfiles(projectId: number): Promise<PipelineProfile[]> {
    return http.get(`/pipeline-profiles?project_id=${projectId}`)
}

export function getPipelineProfile(id: number): Promise<PipelineProfile> {
    return http.get(`/pipeline-profiles/${id}`)
}

export function createPipelineProfile(payload: {
    project_id: number
    name: string
    description?: string
    config: PipelineProfileConfig
}): Promise<PipelineProfile> {
    return http.post('/pipeline-profiles', payload)
}

export function updatePipelineProfile(
    id: number,
    payload: { name?: string; description?: string; config?: PipelineProfileConfig },
): Promise<PipelineProfile> {
    return http.patch(`/pipeline-profiles/${id}`, payload)
}

export function deletePipelineProfile(id: number): Promise<void> {
    return http.del(`/pipeline-profiles/${id}`)
}

/** 算"将来会落库的那个 config_hash", 用于提交前确认这是不是一场新实验。 */
export function previewPipeline(payload: PipelinePreviewPayload): Promise<PipelinePreview> {
    return http.post('/pipeline-preview', payload)
}