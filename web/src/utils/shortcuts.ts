// 快捷键的**接管判定**(M7-3 磨快标签页/金标页的键盘体验)。
//
// 为什么抽出来: 标注工作台与人工金标页各写了一份 `onKeydown`, 两处都只挡了
// "输入框里打字"这一种情况。于是有个真 bug: 按住 Cmd/Ctrl 再按数字(浏览器/系统的
// 快捷键, 比如 Cmd+1 切标签页)也会被当成"改用检索问题/判为忠实", 把标注改掉。
// 现在"该不该接管这次按键"只有这一份实现, 两个页面共用, 并各自有测试钉住。

/** 只取判定需要的字段(KeyboardEvent 天然满足, 测试里也不必造完整事件)。 */
export interface KeyLike {
    key: string
    ctrlKey?: boolean
    metaKey?: boolean
    altKey?: boolean
}

/** 焦点在可输入区域: 里面的数字/字母属于用户内容, 不能被抢走。 */
export function isTypingTarget(target: { tagName?: string; isContentEditable?: boolean } | null | undefined): boolean {
    if (!target) return false
    if (target.isContentEditable) return true
    const tag = (target.tagName ?? '').toUpperCase()
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

/**
 * 统一按键名: 字母统一小写(Shift+N 与 n 同义), 其余原样(数字、`?`、`Enter`)。
 * 之前金标页 switch 里同时写 'n' 与 'N' 两个 case, 就是缺了这一步。
 */
export function normalizeKey(key: string): string {
    if (key.length !== 1) return key
    return /[a-zA-Z]/.test(key) ? key.toLowerCase() : key
}

/**
 * 这次按键该不该被页面接管?
 * - 焦点在输入框/可编辑区 → 不接管(用户在打字)
 * - 带 Ctrl / Meta / Alt 的组合键 → 不接管(那是浏览器或系统的快捷键)
 * - 单个字符键或 `?` → 接管(Shift 组合允许, 否则打不出 `?`)
 */
export function shouldHandleShortcut(
    event: KeyLike | null | undefined,
    target?: { tagName?: string; isContentEditable?: boolean } | null,
): boolean {
    if (!event || !event.key) return false
    if (isTypingTarget(target)) return false
    if (event.ctrlKey || event.metaKey || event.altKey) return false
    return true
}

/**
 * 快捷键帮助的分组(一组 = 一类操作)。
 * 定义放这里而不是某个页面里: 标注工作台与人工金标页都要用同一套展示。
 */
export interface ShortcutGroup {
    title: string
    /** 一行提示里代表这组的短语(弹窗里有完整说明, 这里只求扫一眼能懂)。 */
    line: string
    items: { keys: string[]; action: string }[]
}

/** 把分组拼成页面底部那一行提示。 */
export function helpLine(groups: ShortcutGroup[]): string {
    return `快捷键：${groups.map((group) => group.line).join(' ｜ ')}`
}
