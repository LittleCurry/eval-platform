import { describe, expect, it } from 'vitest'
import { isTypingTarget, normalizeKey, shouldHandleShortcut } from './shortcuts'

describe('isTypingTarget', () => {
    it('输入类元素为真', () => {
        expect(isTypingTarget({ tagName: 'INPUT' })).toBe(true)
        expect(isTypingTarget({ tagName: 'input' })).toBe(true)
        expect(isTypingTarget({ tagName: 'TEXTAREA' })).toBe(true)
        expect(isTypingTarget({ tagName: 'SELECT' })).toBe(true)
        expect(isTypingTarget({ tagName: 'DIV', isContentEditable: true })).toBe(true)
    })

    it('普通元素与空值为假', () => {
        expect(isTypingTarget({ tagName: 'DIV' })).toBe(false)
        expect(isTypingTarget({ tagName: 'BODY' })).toBe(false)
        expect(isTypingTarget(null)).toBe(false)
        expect(isTypingTarget(undefined)).toBe(false)
        expect(isTypingTarget({})).toBe(false)
    })
})

describe('normalizeKey', () => {
    it('字母统一小写(Shift+N 与 n 同义)', () => {
        expect(normalizeKey('N')).toBe('n')
        expect(normalizeKey('B')).toBe('b')
        expect(normalizeKey('n')).toBe('n')
    })

    it('数字与符号原样', () => {
        expect(normalizeKey('1')).toBe('1')
        expect(normalizeKey('?')).toBe('?')
        expect(normalizeKey('Enter')).toBe('Enter')
        expect(normalizeKey('')).toBe('')
    })
})

describe('shouldHandleShortcut', () => {
    const body = { tagName: 'BODY' }
    const input = { tagName: 'INPUT' }

    it('普通页面上的单键接管', () => {
        expect(shouldHandleShortcut({ key: '1' }, body)).toBe(true)
        expect(shouldHandleShortcut({ key: 'n' }, body)).toBe(true)
        expect(shouldHandleShortcut({ key: '?' }, body)).toBe(true)
    })

    it('输入框里不接管(评论里的数字不该变成归因)', () => {
        expect(shouldHandleShortcut({ key: '1' }, input)).toBe(false)
        expect(shouldHandleShortcut({ key: 'n' }, { tagName: 'TEXTAREA' })).toBe(false)
        expect(shouldHandleShortcut({ key: '1' }, { tagName: 'DIV', isContentEditable: true })).toBe(false)
    })

    it('带 Ctrl / Cmd / Alt 的组合键不接管(浏览器自己的快捷键)', () => {
        expect(shouldHandleShortcut({ key: '1', ctrlKey: true }, body)).toBe(false)
        expect(shouldHandleShortcut({ key: '1', metaKey: true }, body)).toBe(false)
        expect(shouldHandleShortcut({ key: 'n', altKey: true }, body)).toBe(false)
        // 没有修饰键的组合才是我们的
        expect(shouldHandleShortcut({ key: '1', ctrlKey: false, metaKey: false, altKey: false }, body)).toBe(true)
    })

    it('Shift 允许(打 ? 必须按住 Shift)', () => {
        expect(shouldHandleShortcut({ key: '?' }, body)).toBe(true)
    })

    it('空事件/空键不接管', () => {
        expect(shouldHandleShortcut(null, body)).toBe(false)
        expect(shouldHandleShortcut(undefined, body)).toBe(false)
        expect(shouldHandleShortcut({ key: '' }, body)).toBe(false)
    })

    it('没传 target 时按"不在输入框里"处理(不崩)', () => {
        expect(shouldHandleShortcut({ key: '1' })).toBe(true)
    })
})
