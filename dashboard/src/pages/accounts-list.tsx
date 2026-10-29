import { useCallback } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { useCursorPagination } from '@/hooks/use-cursor'
import { LoadingState, EmptyState, ErrorState } from '@/components/states'
import { formatShortDate } from '@/lib/utils'
import { api } from '@/lib/api'
import type { Account, PaginatedResponse } from '@/lib/types'
import { ChevronLeft, ChevronRight } from 'lucide-react'

export function AccountsListPage() {
  const fetchAccounts = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams({ limit: '20' })
      if (cursor) params.set('cursor', cursor)
      return api.get<PaginatedResponse<Account>>(`/v1/accounts?${params}`)
    },
    []
  )

  const { data, loading, error, hasNext, hasPrev, currentPage, loadNext, loadPage, refresh } =
    useCursorPagination(fetchAccounts)

  if (error) return <ErrorState error={error} onRetry={refresh} />
  if (loading && !data) return <LoadingState rows={5} />
  if (!data || data.length === 0) return <EmptyState message="No accounts found" />

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Accounts</h1>
        <p className="text-muted-foreground">All wallet accounts</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Account List</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Owner</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Currency</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map(account => (
                <TableRow key={account.id}>
                  <TableCell>
                    <Link
                      to={`/accounts/${account.id}`}
                      className="text-primary hover:underline font-mono text-sm"
                    >
                      {account.owner_ref || account.id.slice(0, 8)}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{account.kind}</Badge>
                  </TableCell>
                  <TableCell>{account.currency}</TableCell>
                  <TableCell>
                    <Badge variant={account.status === 'active' ? 'success' : 'secondary'}>
                      {account.status}
                    </Badge>
                  </TableCell>
                  <TableCell>{formatShortDate(account.created_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div className="flex items-center justify-between">
        <Button
          variant="outline"
          size="sm"
          onClick={() => loadPage(currentPage - 1)}
          disabled={!hasPrev}
          className="gap-1"
        >
          <ChevronLeft className="h-4 w-4" />
          Previous
        </Button>
        <span className="text-sm text-muted-foreground">Page {currentPage + 1}</span>
        <Button
          variant="outline"
          size="sm"
          onClick={loadNext}
          disabled={!hasNext}
          className="gap-1"
        >
          Next
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}
