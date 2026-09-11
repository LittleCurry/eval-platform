// 展示层格式化工具(纯函数, 便于单测)。

export type TagType = 'default' | 'success' | 'warning' | 'error' | 'info'

/** 0.9138 -> "91.4%" */
export function formatPercent(value: number | undefined, digits = 1): string {
    if (value === undefined || Number.isNaN(value)) return '—'
    return `${(value * 100).toFixed(digits)}%`
}

/** 保留小数位, 缺失显示占位符 */
export function formatScore(value: number | undefined, digits = 3): string {
    if (value === undefined || Number.isNaN(value)) return '—'
    return value.toFixed(digits)
}

/** 长哈希截断显示(完整值放 tooltip) */
export function shortHash(hash: string | undefined | null, length = 8): string {
    if (!hash) return '—'
    return hash.slice(0, length)
}

export function statusTagType(status: string | undefined): TagType {
    switch (status) {
        case 'succeeded':
            return 'success'
        case 'running':
            return 'info'
        case 'failed':
            return 'error'
        case 'pending':
            return 'warning'
        default:
            return 'default'
    }
}

export function statusLabel(status: string | undefined): string {
    switch (status) {
        case 'succeeded':
            return '成功'
        case 'running':
            return '运行中'
        case 'failed':
            return '失败'
        case 'pending':
            return '待运行'
        default:
            return status ?? '—'
    }
}

export function difficultyTagType(difficulty: string | undefined): TagType {
    return difficulty === '易' ? 'success' : difficulty === '中' ? 'warning' : difficulty === '难' ? 'error' : 'default'
}

/** ISO 时间 -> "2026-09-10 14:06" */
export function formatDateTime(value: string | undefined | null): string {
    if (!value) return '—'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** 进度文案: (12, 30) -> "12/30" */
export function formatProgress(done: number | undefined, total: number | undefined): string {
    if (total === undefined || total <= 0) return '—'
    const safeDone = Math.min(Math.max(done ?? 0, 0), total)
    return `${safeDone}/${total}`
}

/** 进度百分比(0~100, 保留一位小数), 用于 NProgress */
export function progressPercent(done: number | undefined, total: number | undefined): number {
    if (!total || total <= 0) return 0
    const ratio = ((done ?? 0) / total) * 100
    return Math.max(0, Math.min(100, Math.round(ratio * 10) / 10))
}