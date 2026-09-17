import { describe, expect, it } from 'vitest'
import {
    describeChanges,
    formFromRunSnapshot,
    formToRunPayload,
    rerunGuard,
    sourceFromRunSnapshot,
} from './rerun'
import { emptyProfileForm } from './profile'

/** run #155 的真实快照(截取形状): k=5 + 生成 + 判定。 */
function snapshot155(): Record<string, unknown> {
    return {
        data: { corpus_id: 4, dataset_id: 4 },
        chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
        retrieval: { top_k: 5, reranker: { enabled: false } },
        embedding: { model: 'BAAI/bge-m3', dim: 1024 },
        generation: {
            provider: 'siliconflow', base_url: 'https://api.siliconflow.cn/v1',
            model: 'deepseek-ai/DeepSeek-V3.2', prompt_id: 'qa_zh_v1',
            temperature: 0, max_tokens: 512, max_context_chars: 3000,
        },
        judge: {
            provider: 'siliconflow', base_url: 'https://api.siliconflow.cn/v1',
            model: 'deepseek-ai/DeepSeek-V3.2', claims_prompt_id: 'judge_claims_zh_v1',
            rubric_prompt_id: 'judge_rubric_zh_v1', temperature: 0, max_tokens: 1024,
            max_context_chars: 3000, enable_rubric: true, max_claims: 12,
        },
    }
}

describe('sourceFromRunSnapshot', () => {
    it('取出数据来源(重跑必须用同一份数据)', () => {
        expect(sourceFromRunSnapshot(snapshot155())).toEqual({ datasetId: 4, corpusId: 4 })
    })

    it('缺 data 段或字段不合法时返回 null, 不硬凑出 0', () => {
        expect(sourceFromRunSnapshot(undefined)).toBeNull()
        expect(sourceFromRunSnapshot({})).toBeNull()
        expect(sourceFromRunSnapshot({ data: { dataset_id: 4 } })).toBeNull()
        expect(sourceFromRunSnapshot({ data: { dataset_id: 0, corpus_id: 4 } })).toBeNull()
    })
})

describe('formFromRunSnapshot', () => {
    it('从快照还原 top_k / 切分 / 生成 / 判定', () => {
        const form = formFromRunSnapshot(snapshot155())
        expect(form.topK).toBe(5)
        expect(form.chunking).toEqual({ strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 })
        expect(form.generationEnabled).toBe(true)
        expect(form.generation.model).toBe('deepseek-ai/DeepSeek-V3.2')
        expect(form.judgeEnabled).toBe(true)
        expect(form.judge.claims_prompt_id).toBe('judge_claims_zh_v1')
        expect(form.judge.enable_rubric).toBe(true)
    })

    it('只跑检索的快照里没有 generation/judge 段 → 开关保持关闭(D14: 未启用就不写快照)', () => {
        const form = formFromRunSnapshot({
            data: { corpus_id: 4, dataset_id: 4 },
            chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
            retrieval: { top_k: 1 },
        })
        expect(form.topK).toBe(1)
        expect(form.generationEnabled).toBe(false)
        expect(form.judgeEnabled).toBe(false)
    })

    it('空快照退回默认值, 不产生 undefined 字段', () => {
        const form = formFromRunSnapshot(undefined)
        expect(form).toEqual(emptyProfileForm())
        expect(formFromRunSnapshot({ chunking: 'oops', retrieval: 42 }).topK).toBe(5)
    })

    it('快照里的错类型字段被忽略而不是带进提交体', () => {
        const form = formFromRunSnapshot({
            retrieval: { top_k: '5' },
            chunking: { chunk_size: null, overlap: 50 },
            judge: { model: 123, max_claims: 12 },
        })
        expect(form.topK).toBe(5)
        expect(form.chunking.chunk_size).toBe(500)
        expect(form.chunking.overlap).toBe(50)
        expect(form.judgeEnabled).toBe(true)
        expect(form.judge.model).toBe(emptyProfileForm().judge.model)
    })
})

describe('describeChanges', () => {
    it('只报真正动过的旋钮', () => {
        const baseline = formFromRunSnapshot(snapshot155())
        const changed = { ...baseline, topK: 8 }
        expect(describeChanges(changed, baseline)).toEqual(['top_k 5 → 8'])
    })

    it('多个旋钮一起动时全部列出来(每个都要能被人看见)', () => {
        const baseline = formFromRunSnapshot(snapshot155())
        const changed = {
            ...baseline,
            topK: 8,
            judgeEnabled: false,
            chunking: { ...baseline.chunking, chunk_size: 800 },
        }
        const changes = describeChanges(changed, baseline)
        expect(changes).toContain('top_k 5 → 8')
        expect(changes).toContain('切分 headings/500 → headings/800')
        expect(changes).toContain('判定 开 → 关')
    })

    it('模型或 prompt 变了也要写出来(否则说不清是谁带来的变化)', () => {
        const baseline = formFromRunSnapshot(snapshot155())
        const changed = {
            ...baseline,
            judge: { ...baseline.judge, claims_prompt_id: 'judge_claims_zh_v2' },
        }
        expect(describeChanges(changed, baseline)).toEqual([
            '判定模型/prompt → deepseek-ai/DeepSeek-V3.2 / judge_claims_zh_v2',
        ])
    })

    it('什么都没改时返回空数组', () => {
        const baseline = formFromRunSnapshot(snapshot155())
        expect(describeChanges({ ...baseline }, baseline)).toEqual([])
    })
})

describe('rerunGuard', () => {
    it('一个参数都没改时直接拦下来: 那只会看到噪声', () => {
        const guard = rerunGuard('449ca546', '449ca546', 0)
        expect(guard?.type).toBe('warning')
        expect(guard?.text).toContain('复现')
    })

    it('改了参数但指纹没变(改的东西没进指纹)也要提醒', () => {
        const guard = rerunGuard('449ca546', '449ca546', 1)
        expect(guard?.type).toBe('warning')
        expect(guard?.text).toContain('改动没有进指纹')
    })

    it('指纹不同 = 一次真实验, 不打扰', () => {
        expect(rerunGuard('449ca546', '8f21ab34', 1)).toBeNull()
    })

    it('还没预览出指纹时不误报', () => {
        expect(rerunGuard('449ca546', undefined, 1)).toBeNull()
    })
})

describe('formToRunPayload', () => {
    it('产出提交侧的字段名(摊平的 top_k + corpus/dataset)', () => {
        const form = formFromRunSnapshot(snapshot155())
        const payload = formToRunPayload(form, { datasetId: 4, corpusId: 4 })
        expect(payload).toEqual({
            corpus_id: 4,
            dataset_id: 4,
            chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
            top_k: 5,
            generation: form.generation,
            judge: form.judge,
        })
    })

    it('未启用的阶段提交成 null(与 D14 一致: 不启用就不进快照)', () => {
        const form = { ...emptyProfileForm(), topK: 3 }
        const payload = formToRunPayload(form, { datasetId: 4, corpusId: 4 })
        expect(payload?.generation).toBeNull()
        expect(payload?.judge).toBeNull()
    })

    it('没有数据来源时返回 null(宁可不提交, 也不能跑错数据集)', () => {
        expect(formToRunPayload(emptyProfileForm(), null)).toBeNull()
    })
})
