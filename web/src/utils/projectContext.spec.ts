import { describe, expect, it } from 'vitest'
import {
    PROJECT_STORAGE_KEY,
    emptyProjectHint,
    ownerText,
    parseStoredProjectId,
    projectLabel,
    resolveCurrentProject,
    switchNotice,
} from './projectContext'

describe('parseStoredProjectId', () => {
    it('正常 id 能读出来', () => {
        expect(parseStoredProjectId('1')).toBe(1)
        expect(parseStoredProjectId('42')).toBe(42)
    })

    it('脏数据一律 null, 不猜', () => {
        expect(parseStoredProjectId(null)).toBeNull()
        expect(parseStoredProjectId('')).toBeNull()
        expect(parseStoredProjectId('abc')).toBeNull()
        expect(parseStoredProjectId('0')).toBeNull()
        expect(parseStoredProjectId('-3')).toBeNull()
        expect(parseStoredProjectId('1.5')).toBeNull()
        expect(parseStoredProjectId('NaN')).toBeNull()
    })
})

describe('resolveCurrentProject', () => {
    const projects = [{ id: 1 }, { id: 7 }, { id: 9 }]

    it('记住的项目还在就用它', () => {
        expect(resolveCurrentProject(projects, 7)).toEqual({ projectId: 7, fallback: false })
    })

    it('没有记住过 -> 用第一个, 且不算回退', () => {
        expect(resolveCurrentProject(projects, null)).toEqual({ projectId: 1, fallback: false })
    })

    it('记住的项目没了 -> 回退到第一个, 并**明确告诉调用方发生了回退**', () => {
        // 静默换项目最坑: 人会以为自己在看 A, 其实在看 B
        expect(resolveCurrentProject(projects, 99)).toEqual({ projectId: 1, fallback: true })
    })

    it('一个项目都没有 -> null(页面显示引导, 而不是空表格)', () => {
        expect(resolveCurrentProject([], 3)).toEqual({ projectId: null, fallback: false })
    })
})

describe('展示文案', () => {
    it('下拉文案带 id 与名字', () => {
        expect(projectLabel({ id: 1, name: '知简CRM评测' })).toBe('#1 · 知简CRM评测')
    })

    it('归属: 优先显示邮箱', () => {
        expect(ownerText({ owner_email: 'curry@example.com' })).toBe('由 curry@example.com 创建')
    })

    it('只有 id 时退化成 #id', () => {
        expect(ownerText({ created_by: 3 })).toBe('创建者 #3')
    })

    it('早期项目(created_by 为 NULL)要说清是"未知"而不是留空', () => {
        // M1 那批数据没有 created_by, 显示空白会让人以为是加载失败
        expect(ownerText({})).toContain('创建者未知')
        expect(ownerText({})).toContain('M7 之前')
    })

    it('没有项目时的引导分权限', () => {
        expect(emptyProjectHint(true)).toContain('先建一个项目')
        expect(emptyProjectHint(false)).toContain('管理员')
    })

    it('切换项目要提示"别看串了"', () => {
        const text = switchNotice('#1 · A', '#7 · B')
        expect(text).toContain('#7 · B')
        expect(text).toContain('别看串')
    })

    it('storage key 稳定(改了就等于所有人的项目选择被重置)', () => {
        expect(PROJECT_STORAGE_KEY).toBe('eval.currentProjectId')
    })
})
