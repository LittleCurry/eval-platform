import { describe, expect, it } from 'vitest'
import {
    difficultyTagType,
    formatDateTime,
    formatPercent,
    formatScore,
    shortHash,
    statusLabel,
    statusTagType,
} from './format'

describe('formatPercent', () => {
    it('把 0~1 的比例转成百分比', () => {
        expect(formatPercent(0.913889)).toBe('91.4%')
        expect(formatPercent(1)).toBe('100.0%')
        expect(formatPercent(0)).toBe('0.0%')
    })

    it('缺失或 NaN 显示占位符', () => {
        expect(formatPercent(undefined)).toBe('—')
        expect(formatPercent(Number.NaN)).toBe('—')
    })
})

describe('formatScore', () => {
    it('保留三位小数', () => {
        expect(formatScore(0.834444)).toBe('0.834')
        expect(formatScore(undefined)).toBe('—')
    })
})

describe('shortHash', () => {
    it('截断长哈希并对空值兜底', () => {
        expect(shortHash('b6598ed95e12f5d919ac26a9')).toBe('b6598ed9')
        expect(shortHash('abc', 2)).toBe('ab')
        expect(shortHash('')).toBe('—')
        expect(shortHash(undefined)).toBe('—')
    })
})

describe('statusTagType / statusLabel', () => {
    it('映射 run 状态', () => {
        expect(statusTagType('succeeded')).toBe('success')
        expect(statusTagType('failed')).toBe('error')
        expect(statusTagType('running')).toBe('info')
        expect(statusTagType('pending')).toBe('warning')
        expect(statusTagType('unknown')).toBe('default')
        expect(statusLabel('succeeded')).toBe('成功')
        expect(statusLabel(undefined)).toBe('—')
    })
})

describe('difficultyTagType', () => {
    it('映射难度颜色', () => {
        expect(difficultyTagType('易')).toBe('success')
        expect(difficultyTagType('中')).toBe('warning')
        expect(difficultyTagType('难')).toBe('error')
        expect(difficultyTagType(undefined)).toBe('default')
    })
})

describe('formatDateTime', () => {
    it('格式化 ISO 时间', () => {
        const formatted = formatDateTime('2026-09-10T06:06:38.611686+00:00')
        expect(formatted).toMatch(/^2026-09-10 \d{2}:\d{2}$/)
    })

    it('非法/缺失值兜底', () => {
        expect(formatDateTime(undefined)).toBe('—')
        expect(formatDateTime('not-a-date')).toBe('not-a-date')
    })
})