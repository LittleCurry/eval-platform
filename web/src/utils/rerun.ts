// "改配置重跑"的纯逻辑(M6 闭环的中间一环)。纯函数, 便于单测。
//
// M6 的验收 demo 是"发现 bad case → 打标 → **改配置重跑** → 确认变好 → 销单"。
// 中间那一环以前只能靠手写 curl: 参数名记错一个, 提交出来的就是另一场实验,
// 而 run 的 config_snapshot 才是唯一权威(D7) —— 所以重跑必须**从上一版的快照出发**,
// 只改一个变量, 其余原样带过去。

import type { PipelineProfileConfig, ProfileChunking, ProfileGeneration, ProfileJudge } from '../api/types'
import { emptyProfileForm, formToPreviewPayload, type ProfileForm } from './profile'
import type { PipelinePreviewPayload } from '../api/pipelineProfiles'

/**
 * 从 run 的 config_snapshot 还原出表单。
 *
 * 快照的形状与"落库配置"同构(chunking / retrieval.top_k / generation / judge),
 * 但它是 jsonb 解出来的 unknown: 这里逐段校验类型后再取值, 缺段就退回默认 ——
 * 宁可少带一个阶段, 也不能把 undefined 拼进提交体(那会静默跑成"只检索")。
 */
export function formFromRunSnapshot(snapshot: Record<string, unknown> | undefined): ProfileForm {
    const form = emptyProfileForm()
    if (!snapshot) return form

    const chunking = asRecord(snapshot.chunking)
    if (chunking) {
        form.chunking = {
            strategy: asString(chunking.strategy, form.chunking.strategy),
            chunk_size: asNumber(chunking.chunk_size, form.chunking.chunk_size),
            overlap: asNumber(chunking.overlap, form.chunking.overlap),
            min_chars: asNumber(chunking.min_chars, form.chunking.min_chars),
        }
    }

    const retrieval = asRecord(snapshot.retrieval)
    if (retrieval) form.topK = asNumber(retrieval.top_k, form.topK)

    const generation = asRecord(snapshot.generation)
    if (generation) {
        form.generationEnabled = true
        form.generation = mergeGeneration(form.generation, generation)
    }
    const judge = asRecord(snapshot.judge)
    if (judge) {
        form.judgeEnabled = true
        form.judge = mergeJudge(form.judge, judge)
    }
    return form
}

/** 快照里的数据来源(重跑必须用同一份数据, 否则比的不是同一件事)。 */
export function sourceFromRunSnapshot(
    snapshot: Record<string, unknown> | undefined,
): { datasetId: number; corpusId: number } | null {
    const data = asRecord(snapshot?.data)
    if (!data) return null
    const datasetId = asNumber(data.dataset_id, 0)
    const corpusId = asNumber(data.corpus_id, 0)
    if (datasetId <= 0 || corpusId <= 0) return null
    return { datasetId, corpusId }
}

/**
 * 逐项列出"这次改了哪些旋钮"。
 *
 * 为什么必须显式列出来: 重跑的价值全在"只改一个变量"。如果界面上不写清改了什么,
 * 人很容易连切分一起动了 —— 那之后看到的变化就说不清是谁带来的。
 */
export function describeChanges(current: ProfileForm, baseline: ProfileForm): string[] {
    const changes: string[] = []
    if (current.topK !== baseline.topK) {
        changes.push(`top_k ${baseline.topK} → ${current.topK}`)
    }
    if (current.chunking.strategy !== baseline.chunking.strategy ||
        current.chunking.chunk_size !== baseline.chunking.chunk_size ||
        current.chunking.overlap !== baseline.chunking.overlap ||
        current.chunking.min_chars !== baseline.chunking.min_chars) {
        changes.push(
            `切分 ${baseline.chunking.strategy}/${baseline.chunking.chunk_size}` +
            ` → ${current.chunking.strategy}/${current.chunking.chunk_size}`,
        )
    }
    if (current.generationEnabled !== baseline.generationEnabled) {
        changes.push(`生成 ${baseline.generationEnabled ? '开' : '关'} → ${current.generationEnabled ? '开' : '关'}`)
    } else if (current.generationEnabled &&
        (current.generation.model !== baseline.generation.model ||
            current.generation.temperature !== baseline.generation.temperature)) {
        changes.push(`生成模型/温度 → ${current.generation.model} @${current.generation.temperature}`)
    }
    if (current.judgeEnabled !== baseline.judgeEnabled) {
        changes.push(`判定 ${baseline.judgeEnabled ? '开' : '关'} → ${current.judgeEnabled ? '开' : '关'}`)
    } else if (current.judgeEnabled &&
        (current.judge.model !== baseline.judge.model ||
            current.judge.claims_prompt_id !== baseline.judge.claims_prompt_id)) {
        changes.push(`判定模型/prompt → ${current.judge.model} / ${current.judge.claims_prompt_id}`)
    }
    return changes
}

/** 提交前的护栏: 指纹与上一版相同 = 这是复现, 不会产生任何可验证的变化。 */
export function rerunGuard(
    baselineHash: string,
    previewHash: string | undefined,
    changeCount: number,
): { type: 'warning' | 'info' | 'error'; text: string } | null {
    if (changeCount === 0) {
        return {
            type: 'warning',
            text: '一个参数都没改: 这会是上一版的复现(指纹相同), 闭环页只会看到噪声',
        }
    }
    if (previewHash && baselineHash && previewHash === baselineHash) {
        return {
            type: 'warning',
            text: `算出来的指纹与上一版相同(${previewHash.slice(0, 8)}): 改动没有进指纹, 这是一次复现`,
        }
    }
    return null
}

/** 提交体: 复用配置模板那套映射(字段名与服务端提交侧一致), 少写一遍就少错一处。 */
export function formToRunPayload(
    form: ProfileForm,
    source: { datasetId: number; corpusId: number } | null,
): PipelinePreviewPayload | null {
    if (!source) return null
    return formToPreviewPayload(form, source.corpusId, source.datasetId)
}

function asRecord(value: unknown): Record<string, unknown> | null {
    if (value === null || value === undefined) return null
    if (typeof value !== 'object' || Array.isArray(value)) return null
    return value as Record<string, unknown>
}

function asNumber(value: unknown, fallback: number): number {
    return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function asString(value: unknown, fallback: string): string {
    return typeof value === 'string' && value.length > 0 ? value : fallback
}

// 模板字段是可选的(?: ), 而提交体要的是确定值: 缺省一律补 0/空串,
// 免得把 undefined 拼进 JSON(那会被服务端当成"没传"从而静默改掉语义)。
function mergeGeneration(base: ProfileGeneration, source: Record<string, unknown>): ProfileGeneration {
    return {
        provider: asString(source.provider, base.provider ?? ''),
        base_url: asString(source.base_url, base.base_url ?? ''),
        model: asString(source.model, base.model ?? ''),
        prompt_id: asString(source.prompt_id, base.prompt_id ?? ''),
        temperature: asNumber(source.temperature, base.temperature ?? 0),
        max_tokens: asNumber(source.max_tokens, base.max_tokens ?? 0),
        max_context_chars: asNumber(source.max_context_chars, base.max_context_chars ?? 0),
    }
}

function mergeJudge(base: ProfileJudge, source: Record<string, unknown>): ProfileJudge {
    return {
        provider: asString(source.provider, base.provider ?? ''),
        base_url: asString(source.base_url, base.base_url ?? ''),
        model: asString(source.model, base.model ?? ''),
        claims_prompt_id: asString(source.claims_prompt_id, base.claims_prompt_id ?? ''),
        rubric_prompt_id: asString(source.rubric_prompt_id, base.rubric_prompt_id ?? ''),
        temperature: asNumber(source.temperature, base.temperature ?? 0),
        max_tokens: asNumber(source.max_tokens, base.max_tokens ?? 0),
        max_context_chars: asNumber(source.max_context_chars, base.max_context_chars ?? 0),
        enable_rubric: source.enable_rubric === undefined ? base.enable_rubric === true : source.enable_rubric === true,
        max_claims: asNumber(source.max_claims, base.max_claims ?? 0),
    }
}

/** 切分参数的类型出口(供视图直接用, 免得各处再 import 一次)。 */
export type { ProfileChunking, PipelineProfileConfig }
