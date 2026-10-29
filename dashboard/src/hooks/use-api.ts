import { useState, useEffect, useCallback } from 'react'
import { api, APIError } from '@/lib/api'

interface UseApiState<T> {
  data: T | null
  loading: boolean
  error: APIError | null
}

export function useApi<T>(endpoint: string | null) {
  const [state, setState] = useState<UseApiState<T>>({
    data: null,
    loading: true,
    error: null,
  })

  const refetch = useCallback(async () => {
    if (!endpoint) {
      setState({ data: null, loading: false, error: null })
      return
    }

    setState(prev => ({ ...prev, loading: true, error: null }))

    try {
      const data = await api.get<T>(endpoint)
      setState({ data, loading: false, error: null })
    } catch (err) {
      setState({
        data: null,
        loading: false,
        error: err instanceof APIError ? err : new APIError(500, { title: 'Unknown error', status: 500 }),
      })
    }
  }, [endpoint])

  useEffect(() => {
    refetch()
  }, [refetch])

  return { ...state, refetch }
}
