import { describe, expect, it } from 'vitest'
import type { PipelineProfileConfig } from '../api/types'
import {
    configToForm,
    emptyProfileForm,
    formToConfig,
    formToPreviewPayload,
    profileSummary,
    validateProfileForm,
} from './profile'

describe('emptyProfileForm / formToConfig', () => {
    it('默认只跑检索(generation/judge 写 null, 与 D14 一致)', () => {
        const config = formToConfig(emptyProfileForm())
        expect(config.generation).toBeNull()
        expect(config.judge).toBeNull()
        expect(config.retrieval.top_k).toBe(5)
        expect(config.chunking.chunk_size).toBe(500)
    })

    it('启用后把两个阶段一起带上', () => {
        const form = emptyProfileForm()
        form.generationEnabled = true
        form.judgeEnabled = true
        const config = formToConfig(form)
        expect(config.generation?.prompt_id).toBe('qa_zh_v1')
        expect(config.judge?.claims_prompt_id).toBe('judge_claims_zh_v1')
    })
})

describe('configToForm', () => {
    it('编辑回填: null 段保持关闭', () => {
        const config: PipelineProfileConfig = {
            chunking: { strategy: 'chars', chunk_size: 300, overlap: 30, min_chars: 50 },
            retrieval: { top_k: 3 },
            generation: null,
            judge: null,
        }
        const form = configToForm(config)
        expect(form.chunking.strategy).toBe('chars')
        expect(form.chunking.chunk_size).toBe(300)
        expect(form.topK).toBe(3)
        expect(form.generationEnabled).toBe(false)
        expect(form.judgeEnabled).toBe(false)
    })

    it('有生成/判定则开关打开并覆盖默认值', () => {
        const form = configToForm({
            chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
            retrieval: { top_k: 5 },
            generation: { model: 'my-model', temperature: 0.3 },
            judge: { model: 'my-judge', enable_rubric: false },
        })
        expect(form.generationEnabled).toBe(true)
        expect(form.generation.model).toBe('my-model')
        expect(form.generation.temperature).toBe(0.3)
        expect(form.judgeEnabled).toBe(true)
        expect(form.judge.enable_rubric).toBe(false)
    })

    it('配置缺失时回落到默认表单(不抛错)', () => {
        expect(configToForm(undefined).topK).toBe(5)
    })

    it('往返一趟保持一致', () => {
        const form = emptyProfileForm()
        form.topK = 3
        form.generationEnabled = true
        const roundTrip = configToForm(formToConfig(form))
        expect(roundTrip.topK).toBe(3)
        expect(roundTrip.generationEnabled).toBe(true)
        expect(roundTrip.generation.model).toBe(form.generation.model)
    })
})

describe('formToPreviewPayload', () => {
    it('把 top_k 摊平并带上 corpus/dataset(服务端这个端点吃提交侧字段名)', () => {
        const form = emptyProfileForm()
        const payload = formToPreviewPayload(form, 4, 4)
        expect(payload).toEqual({
            corpus_id: 4,
            dataset_id: 4,
            chunking: form.chunking,
            top_k: 5,
            generation: null,
            judge: null,
        })
    })

    it('缺 corpus/dataset 时返回 null(指纹含数据来源, 缺了没意义)', () => {
        expect(formToPreviewPayload(emptyProfileForm(), null, 4)).toBeNull()
        expect(formToPreviewPayload(emptyProfileForm(), 4, null)).toBeNull()
    })
})

describe('profileSummary', () => {
    it('一眼看出会跑哪几个阶段', () => {
        expect(profileSummary({
            chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
            retrieval: { top_k: 5 },
            generation: null,
            judge: null,
        })).toBe('headings/500 · top_k=5 · 仅检索')
        expect(profileSummary({
            chunking: { strategy: 'headings', chunk_size: 500, overlap: 50, min_chars: 80 },
            retrieval: { top_k: 5 },
            generation: { model: 'm' },
            judge: { model: 'm' },
        })).toBe('headings/500 · top_k=5 · 生成+判定')
    })

    it('配置缺失显示占位符', () => {
        expect(profileSummary(undefined)).toBe('—')
    })
})

describe('validateProfileForm', () => {
    it('默认表单是合法的', () => {
        expect(validateProfileForm(emptyProfileForm())).toBeNull()
    })

    it('拦下"启用判定但没启用生成"(提交侧也会拒绝, 这里提前提示)', () => {
        const form = emptyProfileForm()
        form.judgeEnabled = true
        expect(validateProfileForm(form)).toContain('同时启用生成')
    })

    it('拦下越界的 top_k 与切分', () => {
        const form = emptyProfileForm()
        form.topK = 99
        expect(validateProfileForm(form)).toContain('top_k')
        const form2 = emptyProfileForm()
        form2.chunking.overlap = 999
        expect(validateProfileForm(form2)).toContain('overlap')
        const form3 = emptyProfileForm()
        form3.chunking.chunk_size = 10
        expect(validateProfileForm(form3)).toContain('chunk_size')
    })

    it('启用生成/判定时必填项不能空', () => {
        const form = emptyProfileForm()
        form.generationEnabled = true
        form.generation.model = ''
        expect(validateProfileForm(form)).toContain('model')
        const form2 = emptyProfileForm()
        form2.generationEnabled = true
        form2.judgeEnabled = true
        form2.judge.claims_prompt_id = ''
        expect(validateProfileForm(form2)).toContain('claims_prompt_id')
    })
})