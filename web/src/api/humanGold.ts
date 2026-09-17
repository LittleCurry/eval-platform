import { http } from './client'
import type { HumanGoldScore, JudgeCalibration } from './types'

/** 打分请求体: 分数/备注都可选 —— 没传 = 不改(合并语义), 与后端约定一致。 */
export interface HumanGoldPayload {
    run_id: number
    case_id: number
    annotator: string
    verdict: string
    relevance?: number | null
    helpfulness?: number | null
    note?: string
    project_id?: number
}

/**
 * 某次 run 的人工金标。
 *
 * 金标挂在 (run_id, case_id, annotator) 上而不是题上: judge 判的是**这次 run 生成的那段答案**,
 * 换一次 run(top_k/模型/温度变了)答案就变了, 拿旧金标校准新 run 是错的。
 */
export function listHumanGold(options: { runId: number; annotator?: string }): Promise<HumanGoldScore[]> {
    const query = new URLSearchParams({ run_id: String(options.runId) })
    if (options.annotator) query.set('annotator', options.annotator)
    return http.get(`/human-gold?${query.toString()}`)
}

/**
 * 按 (run, case, annotator) 打分: 已有记录则合并更新。
 * 合并的意思: 这次没传的字段保持原值 —— 所以"过后再补分数""补备注"都是安全的。
 */
export function upsertHumanGold(payload: HumanGoldPayload): Promise<HumanGoldScore> {
    return http.post('/human-gold', payload)
}

/** 传 0 = 撤回这个分数(置 NULL); 传 null/不传 = 不改。 */
export function updateHumanGold(
    id: number,
    payload: { verdict?: string; relevance?: number; helpfulness?: number; note?: string; reviewed?: boolean },
): Promise<HumanGoldScore> {
    return http.patch(`/human-gold/${id}`, payload)
}

export function deleteHumanGold(id: number): Promise<void> {
    return http.del(`/human-gold/${id}`)
}

/** judge 校准报告(κ / 混淆矩阵 / MAE / 均值偏差 / 双人一致性)。 */
export function getJudgeCalibration(options: { runId: number; annotator?: string }): Promise<JudgeCalibration> {
    const query = new URLSearchParams({ run_id: String(options.runId) })
    if (options.annotator) query.set('annotator', options.annotator)
    return http.get(`/judge-calibration?${query.toString()}`)
}
