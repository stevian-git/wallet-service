import { useState, useCallback, useEffect } from 'react'
import { APIError } from '@/lib/api'

interface CursorPaginationState<T> {
  data: T[]
  nextCursor: string | null
  loading: boolean
  error: APIError | null
}

export function useCursorPagination<T>(
  fetchFn: (cursor: string | null) => Promise<{ data: T[]; next_cursor: string | null }>
) {
  const [state, setState] = useState<CursorPaginationState<T>>({
    data: [],
    nextCursor: null,
    loading: true,
    error: null,
  })
  const [cursors, setCursors] = useState<(string | null)[]>([null])
  const [currentPage, setCurrentPage] = useState(0)

  const load = useCallback(async (cursor: string | null, page: number) => {
    setState(prev => ({ ...prev, loading: true, error: null }))
    try {
      const result = await fetchFn(cursor)
      setState({
        data: result.data,
        nextCursor: result.next_cursor,
        loading: false,
        error: null,
      })
      setCurrentPage(page)
    } catch (err) {
      setState(prev => ({
        ...prev,
        loading: false,
        error: err instanceof APIError ? err : new APIError(500, { title: 'Unknown error', status: 500 }),
      }))
    }
  }, [fetchFn])

  const loadPage = useCallback((page: number) => {
    if (page >= 0 && page < cursors.length) {
      load(cursors[page], page)
    }
  }, [cursors, load])

  const loadNext = useCallback(() => {
    if (state.nextCursor) {
      const newCursors = [...cursors.slice(0, currentPage + 1), state.nextCursor]
      setCursors(newCursors)
      load(state.nextCursor, currentPage + 1)
    }
  }, [state.nextCursor, cursors, currentPage, load])

  const refresh = useCallback(() => {
    setCursors([null])
    setCurrentPage(0)
    load(null, 0)
  }, [load])

  useEffect(() => {
    load(null, 0)
  }, [load])

  return {
    ...state,
    currentPage,
    hasNext: state.nextCursor !== null,
    hasPrev: currentPage > 0,
    loadNext,
    loadPage,
    refresh,
  }
}
