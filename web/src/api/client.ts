// 轻量 HTTP 封装: 统一 base、错误解析(后端 {"error": ...})。
// dev 环境经 vite 代理 /api → localhost:8080(见 vite.config.ts), 无 CORS 问题。

const BASE = import.meta.env.VITE_API_BASE ?? '/api/v1'

// ---- 会话注入(M7-1) ----
//
// 为什么用"注册回调"而不是在这里 import 会话模块: 会话模块要用 http 去请求 /auth/me,
// 直接互相 import 会成环。改成 client 暴露两个小钩子, 由会话模块在初始化时挂上 ——
// 依赖方向单向, 也不会因为循环 import 出现"运行时拿到 undefined"的怪问题。

let tokenProvider: () => string | null = () => null
let unauthorizedHandler: (() => void) | null = null

/** 由会话模块注册: 每次请求时怎么取当前 token。 */
export function setTokenProvider(provider: () => string | null) {
    tokenProvider = provider
}

/** 由会话模块注册: 收到 401 时做什么(通常是清会话 + 跳登录页)。 */
export function setUnauthorizedHandler(handler: (() => void) | null) {
    unauthorizedHandler = handler
}

export class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
        super(message)
        this.status = status
    }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        ...(options.headers as Record<string, string> | undefined),
    }
    const token = tokenProvider()
    if (token && !headers.Authorization) {
        headers.Authorization = `Bearer ${token}`
    }
    const res = await fetch(BASE + path, { ...options, headers })

    // 401 = 没登录/登录失效: 交给会话模块统一处理(清 token + 跳登录页)。
    // 注意**不处理 403**: 那是"登录了但没权限", 该由页面自己说明, 不能把人踢去登录。
    if (res.status === 401 && unauthorizedHandler) {
        unauthorizedHandler()
    }

    if (res.status === 204) {
        return undefined as T
    }
    const text = await res.text()
    let body: unknown = null
    if (text) {
        try {
            body = JSON.parse(text)
        } catch {
            body = text
        }
    }
    if (!res.ok) {
        const msg = (body as { error?: string })?.error ?? `请求失败 (HTTP ${res.status})`
        throw new ApiError(res.status, msg)
    }
    return body as T
}

export const http = {
    get: <T>(path: string) => request<T>(path),
    post: <T>(path: string, data?: unknown) =>
        request<T>(path, { method: 'POST', body: data === undefined ? undefined : JSON.stringify(data) }),
    patch: <T>(path: string, data: unknown) =>
        request<T>(path, { method: 'PATCH', body: JSON.stringify(data) }),
    del: <T = void>(path: string) => request<T>(path, { method: 'DELETE' }),
    /** 发送原始文本(如 jsonl 导入), 由调用方指定 Content-Type */
    postRaw: <T>(path: string, raw: string, contentType: string) =>
        request<T>(path, {
            method: 'POST',
            headers: { 'Content-Type': contentType },
            body: raw,
        }),
}