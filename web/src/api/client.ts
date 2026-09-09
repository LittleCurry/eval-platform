// 轻量 HTTP 封装: 统一 base、错误解析(后端 {"error": ...})。
// dev 环境经 vite 代理 /api → localhost:8080(见 vite.config.ts), 无 CORS 问题。

const BASE = import.meta.env.VITE_API_BASE ?? '/api/v1'

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
    const res = await fetch(BASE + path, { ...options, headers })

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