// 前端权限的"统一出口"(M7-3)。
//
// 为什么需要它: M7-1 把权限矩阵铺到了 API 与路由, 但页面里的按钮没管 —— 只读账号
// 点"保存""提交"要等一个来回才知道没权限(403 toast)。这里的职责只有一个:
// **别让人白点**: 该禁用的禁用、该说清的说清。
//
// 真正的边界始终在服务端(rbac.go + 路由分组); 这里只是体验层。

import { computed } from 'vue'
import { can, roleLabel } from '../utils/session'
import { useSession } from './useSession'

export function usePermission() {
    const session = useSession()
    const role = computed(() => session.role.value)

    const canWrite = computed(() => can(role.value, 'write'))
    const canSubmit = computed(() => can(role.value, 'submit'))
    const canDelete = computed(() => can(role.value, 'delete'))
    const canAdmin = computed(() => can(role.value, 'admin'))

    /**
     * 只读时的一句人话(空串 = 有写权限, 页面不必渲染提示)。
     * 说明"为什么不能改"和"找谁" —— 只说"无权限"会让人以为是登录问题。
     */
    const readOnlyHint = computed(() => {
        if (!session.loggedIn.value) return ''
        if (canWrite.value) return ''
        return `当前角色是「${roleLabel(role.value)}」：能看报告、对比与标注结果，但不能改动数据。` +
            '需要改数据的话，让管理员在「用户管理」里把角色改成「编辑者」。'
    })

    return { role, canWrite, canSubmit, canDelete, canAdmin, readOnlyHint }
}
