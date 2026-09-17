import { http } from './client'
import type { Role, SessionUser, UserAccount } from './types'

/** 登录/注册成功后的响应(M7-1)。 */
export interface AuthResult {
    token: string
    expires_at: string
    user: UserAccount
}

export interface AuthStatus {
    bootstrap_needed: boolean
    token_ttl_hours: number
}

/** 公开: 前端据此决定显示"登录"还是"首次创建管理员"。 */
export function getAuthStatus(): Promise<AuthStatus> {
    return http.get('/auth/status')
}

export function login(email: string, password: string): Promise<AuthResult> {
    return http.post('/auth/login', { email, password })
}

/**
 * 仅在系统还没有任何账号时可用(首个用户自动成为管理员)。
 * 之后加人走 /users(管理员)。
 */
export function register(email: string, name: string, password: string): Promise<AuthResult> {
    return http.post('/auth/register', { email, name, password })
}

/** 当前登录用户 + 能力清单(刷新页面时用它确认会话仍然有效)。 */
export function getMe(): Promise<{ user: UserAccount; actions: string[] }> {
    return http.get('/auth/me')
}

/** 无状态 JWT: 服务端不做撤销, 这里只是走个形式(客户端负责丢弃 token)。 */
export function logout(): Promise<{ message: string }> {
    return http.post('/auth/logout')
}

// ---- 用户管理(仅管理员) ----

export function listUsers(): Promise<UserAccount[]> {
    return http.get('/users')
}

export function createUser(payload: {
    email: string
    name?: string
    password: string
    role: Role
}): Promise<UserAccount> {
    return http.post('/users', payload)
}

/**
 * 局部更新(与 M6 一致的指针语义): 没传的字段不改。
 * password 是**重置**入口: 传了才改。
 */
export function updateUser(
    id: number,
    payload: { name?: string; role?: Role; disabled?: boolean; password?: string },
): Promise<UserAccount> {
    return http.patch(`/users/${id}`, payload)
}

export function deleteUser(id: number): Promise<void> {
    return http.del(`/users/${id}`)
}

export type { SessionUser }
