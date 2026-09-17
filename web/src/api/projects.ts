import { http } from './client'
import type { Project } from './types'

/**
 * 项目列表(M7-2)。
 *
 * 全员可见(D24): 不按用户过滤 —— 项目是"数据隔离 + 归属"的单位, 不是权限单位。
 * 谁建的写在 created_by/owner_email 里, 出事找得到人。
 */
export function listProjects(): Promise<Project[]> {
    return http.get('/projects')
}

/** 项目被删时这里会明确 404 —— 前端据此提示并切回第一个项目, 而不是静默串项目。 */
export function getProject(id: number): Promise<Project> {
    return http.get(`/projects/${id}`)
}

export function createProject(payload: { name: string; description?: string }): Promise<Project> {
    return http.post('/projects', payload)
}
