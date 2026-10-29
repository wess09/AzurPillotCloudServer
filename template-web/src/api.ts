// 管理 API 客户端：统一携带 Bearer Token、解析统一错误信封。
const TOKEN_KEY = 'ap_template_admin_token'

export class ApiError extends Error {
  status: number
  code: string
  details: Record<string, unknown>

  constructor(status: number, code: string, message: string, details: Record<string, unknown>) {
    super(message)
    this.status = status
    this.code = code
    this.details = details
  }

  get isAuthError(): boolean {
    return this.status === 401 || this.status === 403
  }
}

interface ErrorEnvelope {
  error?: { code?: string; message?: string; details?: Record<string, unknown> }
}

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? ''
}

export function setToken(token: string): void {
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) headers.authorization = `Bearer ${token}`
  if (body !== undefined) headers['content-type'] = 'application/json'
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) {
    const envelope = (data ?? {}) as ErrorEnvelope
    const err = new ApiError(
      res.status,
      envelope.error?.code ?? 'HTTP_ERROR',
      envelope.error?.message ?? `请求失败（HTTP ${res.status}）`,
      envelope.error?.details ?? {},
    )
    if (err.isAuthError) {
      setToken('')
      window.dispatchEvent(new Event('template-auth-expired'))
    }
    throw err
  }
  return data as T
}

export function get<T>(path: string): Promise<T> {
  return request<T>('GET', path)
}

export function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('POST', path, body)
}

export function patch<T>(path: string, body: unknown): Promise<T> {
  return request<T>('PATCH', path, body)
}

export function del<T>(path: string): Promise<T> {
  return request<T>('DELETE', path)
}
