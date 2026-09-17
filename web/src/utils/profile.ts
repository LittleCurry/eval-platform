// 配置模板的纯逻辑(M5-2): 表单 <-> 配置 <-> 预览请求体 的双向映射。
//
// 为什么单独抽出来: 这三者字段名并不一致 —— 表单是给人填的扁平结构、配置是落库结构
// (retrieval 嵌一层 top_k)、预览请求体又是提交侧字段名(扁平 top_k + corpus/dataset)。
// 映射写错了不会报错, 只会悄悄提交出一场"配置跟你想的不一样"的实验, 所以每一步都要有单测。

import type {
    PipelinePreviewPayload,
} from '../api/pipelineProfiles'
import type {
    PipelineProfileConfig,
    ProfileChunking,
    ProfileGeneration,
    ProfileJudge,
} from '../api/types'

/** 表单状态: 与配置同构, 但生成/判定用开关 + 展开的字段, 便于"不启用就整段不填"。 */
export interface ProfileForm {
    chunking: ProfileChunking
    topK: number
    generationEnabled: boolean
    generation: ProfileGeneration
    judgeEnabled: boolean
    judge: ProfileJudge
}

/** 与 worker 侧一致的默认值(切分) —— 模板默认值必须与服务端默认值同源。 */
export function emptyProfileForm(): ProfileForm {
    return {
        chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
        topK: 5,
        generationEnabled: false,
        generation: { provider: 'siliconflow', base_url: 'https://api.siliconflow.cn/v1', model: 'deepseek-ai/DeepSeek-V3.2', prompt_id: 'qa_zh_v1', temperature: 0, max_tokens: 512, max_context_chars: 3000 },
        judgeEnabled: false,
        judge: { provider: 'siliconflow', base_url: 'https://api.siliconflow.cn/v1', model: 'deepseek-ai/DeepSeek-V3.2', claims_prompt_id: 'judge_claims_zh_v1', rubric_prompt_id: 'judge_rubric_zh_v2', temperature: 0, max_tokens: 1024, max_context_chars: 3000, enable_rubric: true, max_claims: 12 },
    }
}

/** 落库配置 -> 表单(编辑时回填)。null 段 = 该阶段未启用。 */
export function configToForm(config: PipelineProfileConfig | undefined): ProfileForm {
    const form = emptyProfileForm()
    if (!config) return form
    if (config.chunking) form.chunking = { ...form.chunking, ...config.chunking }
    if (config.retrieval?.top_k) form.topK = config.retrieval.top_k
    if (config.generation) {
        form.generationEnabled = true
        form.generation = { ...form.generation, ...config.generation }
    }
    if (config.judge) {
        form.judgeEnabled = true
        form.judge = { ...form.judge, ...config.judge }
    }
    return form
}

/** 表单 -> 落库配置。未启用的阶段写 null(= 提交时不带该段, 与 D14 语义一致)。 */
export function formToConfig(form: ProfileForm): PipelineProfileConfig {
    return {
        chunking: { ...form.chunking },
        retrieval: { top_k: form.topK },
        generation: form.generationEnabled ? { ...form.generation } : null,
        judge: form.judgeEnabled ? { ...form.judge } : null,
    }
}

/** 表单 -> 预览请求体(注意 top_k 要摊平, 并带上 corpus/dataset)。 */
export function formToPreviewPayload(
    form: ProfileForm,
    corpusId: number | null,
    datasetId: number | null,
): PipelinePreviewPayload | null {
    if (!corpusId || !datasetId) return null
    return {
        corpus_id: corpusId,
        dataset_id: datasetId,
        chunking: { ...form.chunking },
        top_k: form.topK,
        generation: form.generationEnabled ? { ...form.generation } : null,
        judge: form.judgeEnabled ? { ...form.judge } : null,
    }
}

/**
 * 列表里的一行配置摘要: "headings/500 · top_k=5 · 生成+判定"。
 * 模板列表的第一眼就该看出"这个模板会跑哪几个阶段", 而不是点进去才知道。
 */
export function profileSummary(config: PipelineProfileConfig | undefined): string {
    if (!config) return '—'
    const chunking = config.chunking
    const parts = [
        `${chunking?.strategy ?? '—'}/${chunking?.chunk_size ?? '—'}`,
        `top_k=${config.retrieval?.top_k ?? '—'}`,
    ]
    const stages: string[] = []
    if (config.generation) stages.push('生成')
    if (config.judge) stages.push('判定')
    parts.push(stages.length ? stages.join('+') : '仅检索')
    return parts.join(' · ')
}

/** 表单校验: 只拦"明显跑不起来"的组合, 其余交给服务端(它是唯一权威)。 */
export function validateProfileForm(form: ProfileForm): string | null {
    if (!form.chunking.chunk_size || form.chunking.chunk_size < 100) return 'chunk_size 至少 100'
    if (form.chunking.overlap < 0 || form.chunking.overlap >= form.chunking.chunk_size) {
        return 'overlap 必须小于 chunk_size'
    }
    if (!form.topK || form.topK < 1 || form.topK > 50) return 'top_k 必须在 1~50 之间'
    // 判定必须有答案: 提交侧会拒绝, 这里提前提示, 免得用户填完才被服务端打回
    if (form.judgeEnabled && !form.generationEnabled) return '启用判定时必须同时启用生成(判定需要有答案)'
    if (form.generationEnabled && !form.generation.model) return '启用生成时必须填 model'
    if (form.judgeEnabled && !form.judge.claims_prompt_id) return '启用判定时必须填 claims_prompt_id'
    return null
}