// 标注闭环的展示口径(M6-4)。纯函数, 便于单测。
//
// 闭环页只回答两个问题, 每个函数都为它们服务:
//  1. 我标成 fixed 的题, 现在能不能销单?(eligible / blocked_reason)
//  2. 如果结论不可信, 是哪一步没做对?(同配置 / 少归因 / 缺标注)

import type { ClosureEvidence, ClosureRecord, ClosureReport, ClosureSummary, Run } from '../api/types'
import type { TagType } from './format'
import { flagLabel } from './report'

export const CLOSURE_VERDICT_LABELS: Record<string, string> = {
    improved: '真的变好了',
    stable: '没变化',
    worsened: '反而变坏',
    changed: '换了病因',
    unverifiable: '判断不了',
}

export const CLOSURE_VERDICT_TYPES: Record<string, TagType> = {
    improved: 'success',
    stable: 'default',
    worsened: 'error',
    changed: 'warning',
    unverifiable: 'info',
}

export function closureVerdictLabel(verdict: string): string {
    return CLOSURE_VERDICT_LABELS[verdict] ?? verdict
}

export function closureVerdictType(verdict: string): TagType {
    return CLOSURE_VERDICT_TYPES[verdict] ?? 'default'
}

/** 标签列表 -> 人话(空 = 干净)。 */
export function flagsText(flags: string[] | null | undefined): string {
    if (!flags || flags.length === 0) return '干净'
    return flags.map((flag) => flagLabel(flag)).join(' / ')
}

/** 结论行: "已修 3 题中 3 题确认变好, 可销单"。 */
export function closureSummaryLine(summary: ClosureSummary): string {
    if (summary.annotated === 0) return '这次 run 上还没有可核对的标注'
    const parts = [`已标 ${summary.annotated} 题`]
    if (summary.fixed_total > 0) {
        parts.push(`其中 fixed ${summary.fixed_total} 题, ${summary.eligible_for_verify} 题确认变好可销单`)
    }
    if (summary.worsened > 0) parts.push(`⚠ ${summary.worsened} 题反而变坏`)
    if (summary.changed > 0) parts.push(`${summary.changed} 题换了病因`)
    if (summary.unverifiable > 0) parts.push(`${summary.unverifiable} 题判断不了`)
    return parts.join(' ｜ ')
}

/** 下一步动作建议(闭环的意义就在于把人推到下一步)。 */
export function closureActionText(summary: ClosureSummary): string {
    if (summary.annotated === 0) {
        return '先到标注工作台把 bad case 标出来, 再回来看它们在新 run 里有没有变好'
    }
    if (summary.eligible_for_verify > 0) {
        return `有 ${summary.eligible_for_verify} 题已确认修好: 可以一键推进到 verified, 这条待办就算走完了`
    }
    if (summary.worsened > 0) {
        return '没有可销单的题, 而且有题变坏了 —— 先查这些回归, 再谈修复效果'
    }
    return '暂时没有可销单的题: 要么修复还没生效, 要么这道题坏在别的环节(看左边的标签变化)'
}

/**
 * 单条指标证据 -> 人话: "Recall@k 0% → 100%"。
 * 不可比时不给数字 —— "没判定"绝不能被读成 0 分。
 */
export function evidenceText(evidence: ClosureEvidence, labelOf: (metric: string) => string): string {
    if (!evidence.comparable) return `${labelOf(evidence.metric)} 不可比`
    return `${labelOf(evidence.metric)} ${formatEvidenceValue(evidence.left)} → ${formatEvidenceValue(evidence.right)}`
}

/** 比例的显示: 0~1 之间按百分比, 其余按小数(避免把"2.5 分"写成 250%)。 */
export function formatEvidenceValue(value: number): string {
    if (Number.isNaN(value)) return '—'
    if (value >= 0 && value <= 1) return `${(value * 100).toFixed(0)}%`
    return value.toFixed(2)
}

/**
 * 挑出最该看的几条证据, 顺序即优先级: 变坏 > 变好 > 没动 > 不可比; 同档按变化幅度。
 *
 * 为什么把"变坏"排在"变好"前面: 一次改动如果既修好了 A 又弄坏了 B, 人第一眼该看到 B。
 * 为什么不把"没动"隐藏掉: 得让人确认"确实没动", 而不是怀疑数据被挑了。
 */
export function evidenceHighlights(evidence: ClosureEvidence[], limit = 3): ClosureEvidence[] {
    if (!evidence || evidence.length === 0) return []
    const rank = (item: ClosureEvidence) => {
        if (!item.comparable) return 3
        if (item.direction === 'worse') return 0
        if (item.direction === 'better') return 1
        return 2
    }
    return [...evidence]
        .sort((left, right) => {
            const diff = rank(left) - rank(right)
            return diff !== 0 ? diff : Math.abs(right.delta) - Math.abs(left.delta)
        })
        .slice(0, limit)
}

/** 一道题能不能销单, 以及不能的话卡在哪。 */
export function verifyHint(record: ClosureRecord): { ok: boolean; text: string; type: TagType } {
    if (record.eligible_for_verify) {
        return { ok: true, text: '可以销单', type: 'success' }
    }
    if (record.blocked_reason) {
        return { ok: false, text: record.blocked_reason, type: closureVerdictType(record.verdict) }
    }
    return { ok: false, text: '还不能销单', type: 'default' }
}

/**
 * 候选 run 选项: 同评测集优先, 并标出"同配置(复现)"。
 *
 * 为什么要把同配置的摆在明面上: 拿两次配置相同的 run 做闭环, 得到的"变化"只可能是噪声,
 * 而它长得跟"真有效果"一模一样 —— 必须在选择列表里就写清楚。
 */
export function candidateRunOptions(
    runs: Run[],
    baseline: Run | null,
    baselineId: number,
): { label: string; value: number }[] {
    const others = runs.filter((run) => run.id !== baselineId && run.status === 'succeeded')
    // 不知道基线是哪次时, 不假装知道谁"同集": 全部按跨集标注(反正后端会判不可比)
    const same = baseline ? others.filter((run) => run.dataset_id === baseline.dataset_id) : []
    const rest = baseline ? others.filter((run) => run.dataset_id !== baseline.dataset_id) : others
    const decorate = (run: Run, withDataset: boolean) => {
        const tags = [`#${run.id}`]
        if (withDataset) tags.push(`dataset ${run.dataset_id}`)
        else tags.push('同集')
        if (baseline && run.config_hash === baseline.config_hash) tags.push('同配置=复现')
        return { label: tags.join(' · '), value: run.id }
    }
    return [
        ...same.map((run) => decorate(run, false)),
        ...rest.map((run) => decorate(run, true)),
    ]
}

/** 页面顶部的护栏提示: 先拦最致命的(不可比 > 复现 > 归因缺失)。 */
export function closureGuard(report: ClosureReport): { type: TagType; text: string } | null {
    if (!report.comparable) {
        return { type: 'error', text: `这次对比不成立：${report.reason}` }
    }
    if (report.attribution_missing) {
        return { type: 'error', text: report.attribution_missing }
    }
    if (report.same_config) {
        return {
            type: 'warning',
            text: '两次 run 的配置指纹完全相同：这是复现, 不是实验。这里的"变好/变坏"只可能是噪声, ' +
                '要验证修复请选一次真的改了配置的 run。',
        }
    }
    return null
}

/** 汇总卡: 闭环页顶部四个数字。 */
export function closureCards(summary: ClosureSummary): { label: string; value: number; hint: string }[] {
    return [
        { label: '可销单', value: summary.eligible_for_verify, hint: 'fixed 且确认变好' },
        { label: '反而变坏', value: summary.worsened, hint: '这次改动弄坏的' },
        { label: '换了病因', value: summary.changed, hint: '病没好, 只是变了' },
        { label: '判断不了', value: summary.unverifiable, hint: '缺题或缺归因' },
    ]
}