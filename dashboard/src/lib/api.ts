import type { ProblemDetail } from './types'

export class APIError extends Error {
  status: number
  detail?: ProblemDetail

  constructor(status: number, detail?: ProblemDetail) {
    super(detail?.title || 'API Error')
    this.status = status
    this.detail = detail
  }
}

function getApiKey(): string {
  const envKey = import.meta.env.VITE_API_KEY
  if (envKey) return envKey
  return localStorage.getItem('apiKey') || ''
}

export function setApiKey(key: string) {
  localStorage.setItem('apiKey', key)
}

export function getStoredApiKey(): string {
  return localStorage.getItem('apiKey') || ''
}

async function request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
  const baseUrl = import.meta.env.VITE_API_URL || 'http://localhost:8080'
  const apiKey = getApiKey()

  const headers: HeadersInit = {
    'Content-Type': 'application/json',
    ...(apiKey && { 'X-API-Key': apiKey }),
    ...options.headers,
  }

  const response = await fetch(`${baseUrl}${endpoint}`, {
    ...options,
    headers,
  })

  if (!response.ok) {
    const error = await response.json().catch(() => ({
      title: 'Request failed',
      status: response.status,
    }))
    throw new APIError(response.status, error)
  }

  return response.json()
}

export const api = {
  get: <T>(endpoint: string) => request<T>(endpoint),

  post: <T>(endpoint: string, body: unknown) =>
    request<T>(endpoint, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
}

export type { ProblemDetail }
