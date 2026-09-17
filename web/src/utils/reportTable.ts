// 报告页两张表格的列定义(M7-3 收口)。
//
// 为什么要抽出来: 以前列宽定义在视图里, 而 `scroll-x` 是旁边两个手写常数
// (1086 / 776) —— 加一列忘了改这个数, 表格就把列挤窄(文字被裁成"…"),
// 而且只有靠肉眼看才能发现。现在:
//   1. 列宽只有这一份数据(SPEC), scroll-x 由它**推导**(scrollX);
//   2. 数字化列统一右对齐(MRR 0.783 与 Recall 67.4% 应该像一列数字, 而不是一列文字);
//   3. 紧凑表头(断言 1/0/0、rubric R5 H4)配表头悬浮说明 —— 看懂的人一眼过, 看不懂的人不用问人;
//   4. 渲染函数与列宽放在一起, 于是它可以被单测直接调用(不需要挂 DOM)。
//
// 视图只负责"把数据放进去 + 点详情打开抽屉"。

import { h } from 'vue'
import type { VNodeChild } from 'vue'
import type { DataTableColumns } from 'naive-ui'
import { NButton, NTag, NText, NTooltip } from 'naive-ui'
import type { RunCaseResult } from '../api/types'
import { difficultyTagType, formatPercent, formatScore } from './format'
import { NEUTRAL_TEXT, tagHex } from './palette'
import { answerSnippet, claimCounter, flagLabel, flagTagType, primaryFlag, rubricSummary } from './report'

/** 自适应列(吃剩余宽度)至少要留出这么宽, 否则"问题"会被压成一句话都看不全。 */
export const FLEX_MIN_WIDTH = 200

export interface ColumnSpec {
    key: string
    title: string
    /** 固定列宽(px); 省略 = 自适应列(只允许一个, 否则没人知道剩余宽度给谁) */
    width?: number
    fixed?: 'left' | 'right'
    align?: 'left' | 'right' | 'center'
    /** 内容可能很长: 交给表格省略 + 悬浮显示全文 */
    ellipsis?: boolean
    /** 表头口径说明(鼠标悬浮表头) */
    hint?: string
    /** 该列的下限宽度: 只给测试用 —— 以后有人把它改窄到装不下内容时会红 */
    minWidth?: number
}

const PRIMARY_FLAG_HINT = '主因 = 归因标签里的第一条(见 process.md D16)：检索没命中 → 检索问题，命中却答错 → 生成/幻觉'

/** 全量单题结果表: 检索 + 判定 + 答案, 一屏看完一题。 */
export const CASE_TABLE_SPECS: ColumnSpec[] = [
    { key: 'qid', title: 'qid', width: 96, fixed: 'left', minWidth: 80 },
    { key: 'question', title: '问题', ellipsis: true, minWidth: FLEX_MIN_WIDTH },
    { key: 'difficulty', title: '难度', width: 64, minWidth: 56 },
    { key: 'recall', title: 'Recall', width: 82, align: 'right', minWidth: 72 },
    { key: 'rr', title: 'MRR', width: 72, align: 'right', minWidth: 64 },
    { key: 'primary_flag', title: '主因', width: 132, hint: PRIMARY_FLAG_HINT, minWidth: 120 },
    {
        key: 'claims',
        title: '断言',
        width: 96,
        hint: '断言三段计数「有据/无据/无关」：无据 = 资料里找不到依据（幻觉），出现就会标红',
        minWidth: 88,
    },
    {
        key: 'rubric',
        title: 'rubric',
        width: 76,
        hint: 'judge 的 rubric 打分：R = relevance，H = helpfulness，各 1–5 分',
        minWidth: 64,
    },
    { key: 'answer', title: '答案', width: 180, ellipsis: true, minWidth: 140 },
    { key: 'actions', title: '操作', width: 92, fixed: 'right', minWidth: 92 },
]

/** 最差用例表: 只看"这题坏在哪", 所以换成首个命中位次而不是 MRR/断言。 */
export const WORST_TABLE_SPECS: ColumnSpec[] = [
    { key: 'qid', title: 'qid', width: 96, fixed: 'left', minWidth: 80 },
    { key: 'question', title: '问题', ellipsis: true, minWidth: FLEX_MIN_WIDTH },
    { key: 'difficulty', title: '难度', width: 64, minWidth: 56 },
    { key: 'recall', title: 'Recall', width: 82, align: 'right', minWidth: 72 },
    {
        key: 'first_hit_rank',
        title: '首个命中',
        width: 110,
        hint: 'gold 里排得最靠前的那个出现在第几位：未命中 = 这一次检索没把正确资料捞回来',
        minWidth: 96,
    },
    { key: 'primary_flag', title: '主因', width: 132, hint: PRIMARY_FLAG_HINT, minWidth: 120 },
    { key: 'actions', title: '操作', width: 92, fixed: 'right', minWidth: 92 },
]

/** 固定列宽之和(自适应列不计)。 */
export function fixedWidthTotal(specs: ColumnSpec[]): number {
    return specs.reduce((sum, spec) => sum + (spec.width ?? 0), 0)
}

/**
 * 表格的 scroll-x: 固定列之和 + 自适应列的下限宽度。
 * 比这个还窄的窗口就横向滚动, 而不是把右边的列裁掉/压成"…"。
 */
export function scrollX(specs: ColumnSpec[], flexMin = FLEX_MIN_WIDTH): number {
    return fixedWidthTotal(specs) + flexMin
}

export const CASE_TABLE_SCROLL_X = scrollX(CASE_TABLE_SPECS)
export const WORST_TABLE_SCROLL_X = scrollX(WORST_TABLE_SPECS)

/** 单元格渲染函数表: 每个 spec.key 都要有一条(测试里会逐个核对)。 */
export type CellRenderers = Record<string, ((row: RunCaseResult) => VNodeChild) | undefined>

/**
 * 表头: 有 hint 的列渲染成一个带 tooltip 的标题。
 * 不把说明塞进表头文字里 —— 表头一长, 列就得跟着变宽(而列宽刚刚才收过一遍)。
 * naive-ui 的 `title` 允许传函数(Header.mjs: renderTitle), 所以这里不需要额外的列插槽。
 */
function columnTitle(spec: ColumnSpec): string | (() => VNodeChild) {
    if (!spec.hint) return spec.title
    return () => h(NTooltip, {}, {
        trigger: () => h('span', { style: 'cursor: help' }, spec.title),
        default: () => spec.hint,
    })
}

/** 列定义 = 列规格(宽/对齐/固定/表头提示) + 渲染函数。 */
export function buildColumns(
    specs: ColumnSpec[],
    cells: CellRenderers,
): DataTableColumns<RunCaseResult> {
    return specs.map((spec) => ({
        key: spec.key,
        title: columnTitle(spec),
        width: spec.width,
        fixed: spec.fixed,
        align: spec.align,
        ellipsis: spec.ellipsis ? { tooltip: true } : undefined,
        render: cells[spec.key] ?? (() => ''),
    }))
}

// ---- 单元格 ----

/** 主因标签(flags[0]): 报告的第一眼应该是"这题的锅在谁头上"。 */
export function primaryFlagCell(row: RunCaseResult): VNodeChild {
    const flag = primaryFlag(row.flags)
    if (!flag) return h(NText, { depth: 3 }, { default: () => '—' })
    return h('span', { title: (row.flags ?? []).join(' → ') }, [
        h(NTag, { size: 'small', type: flagTagType(flag) }, { default: () => flagLabel(flag) }),
    ])
}

export function rubricCell(row: RunCaseResult): VNodeChild {
    const summary = rubricSummary(row.judge)
    if (summary === null) return h(NText, { depth: 3 }, { default: () => '—' })
    const reason = row.judge?.rubric?.reason ?? ''
    return h('span', { title: reason }, [summary])
}

/**
 * 断言列用紧凑计数 "支持/无据/无关"(列宽只有 96px, 长文案会被裁)。
 * 出现"无据/无关"时标红 —— 一行坏答案在表里第一眼就该被看见。
 */
export function claimsCell(row: RunCaseResult): VNodeChild {
    const counts = claimCounter(row.judge)
    if (counts === null) return h(NText, { depth: 3 }, { default: () => '—' })
    const title = `${counts.supported} 有据 / ${counts.unsupported} 无据 / ${counts.irrelevant} 无关`
    const text = `${counts.supported}/${counts.unsupported}/${counts.irrelevant}`
    if (counts.unsupported > 0 || counts.irrelevant > 0) {
        return h('span', { title, style: `color: ${tagHex('error')}; font-weight: 600` }, text)
    }
    return h('span', { title, style: `color: ${NEUTRAL_TEXT}` }, text)
}

export function answerCell(row: RunCaseResult): VNodeChild {
    // 不截断(交给列宽省略 + 悬浮 tooltip), 只做单行化, 免得 tooltip 也只剩半句
    return answerSnippet(row.answer, 0) ?? h(NText, { depth: 3 }, { default: () => '—' })
}

export function difficultyCell(row: RunCaseResult): VNodeChild {
    if (!row.difficulty) return null
    return h(NTag, { size: 'small', type: difficultyTagType(row.difficulty) }, { default: () => row.difficulty })
}

function actionCell(onOpen: (row: RunCaseResult) => void) {
    return (row: RunCaseResult): VNodeChild =>
        h(NButton, { size: 'small', quaternary: true, onClick: () => onOpen(row) }, { default: () => '详情' })
}

export interface ReportTableHandlers {
    openDetail: (row: RunCaseResult) => void
}

/** 全量单题结果表(检索 + 判定 + 答案)。 */
export function caseTableColumns(handlers: ReportTableHandlers): DataTableColumns<RunCaseResult> {
    return buildColumns(CASE_TABLE_SPECS, {
        qid: (row) => row.qid,
        question: (row) => row.question,
        difficulty: difficultyCell,
        recall: (row) => formatPercent(row.metrics?.recall),
        rr: (row) => formatScore(row.metrics?.reciprocal_rank),
        primary_flag: primaryFlagCell,
        claims: claimsCell,
        rubric: rubricCell,
        answer: answerCell,
        actions: actionCell(handlers.openDetail),
    })
}

/** 最差用例表(按 Recall 升序, 看"坏在哪")。 */
export function worstTableColumns(handlers: ReportTableHandlers): DataTableColumns<RunCaseResult> {
    return buildColumns(WORST_TABLE_SPECS, {
        qid: (row) => row.qid,
        question: (row) => row.question,
        difficulty: difficultyCell,
        recall: (row) => formatPercent(row.metrics?.recall),
        first_hit_rank: (row) =>
            row.metrics?.first_hit_rank ? `第 ${row.metrics.first_hit_rank} 位` : '未命中',
        primary_flag: primaryFlagCell,
        actions: actionCell(handlers.openDetail),
    })
}
