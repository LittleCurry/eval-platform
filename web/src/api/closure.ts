import { http } from './client'
import type { ClosureReport } from './types'

/**
 * 标注闭环报告(M6-4): 标成 fixed 的题, 在新一次 run 里真的好了吗?
 *
 * 方向不能反: baseline = 打标注的那次 run(待办所在), candidate = 改完之后的新 run。
 * 反了会把"我修好的题"读成"我弄坏的题"。
 */
export function getClosure(options: {
    baseline: number
    candidate: number
    status?: string
}): Promise<ClosureReport> {
    const query = new URLSearchParams({
        baseline: String(options.baseline),
        candidate: String(options.candidate),
    })
    if (options.status) query.set('status', options.status)
    return http.get(`/closure?${query.toString()}`)
}
