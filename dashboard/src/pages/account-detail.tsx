import { useParams, Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { useCursorPagination } from '@/hooks/use-cursor'
import { LoadingState, LoadingCards, EmptyState, ErrorState } from '@/components/states'
import { formatIDR, formatDate } from '@/lib/utils'
import { api } from '@/lib/api'
import type { Account, Balance, LedgerEntry, PaginatedResponse } from '@/lib/types'
import { useCallback } from 'react'
import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react'

export function AccountDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data: account, loading: accLoading, error: accError, refetch: refetchAcc } =
    useApi<Account>(id ? `/v1/accounts/${id}` : null)
  const { data: balance, loading: balLoading, error: balError } =
    useApi<Balance>(id ? `/v1/accounts/${id}/balance` : null)

  const fetchEntries = useCallback(
    (cursor: string | null) => {
      if (!id) return Promise.resolve({ data: [], next_cursor: null })
      const params = new URLSearchParams({ limit: '20' })
      if (cursor) params.set('cursor', cursor)
      return api.get<PaginatedResponse<LedgerEntry>>(
        `/v1/accounts/${id}/entries?${params}`
      )
    },
    [id]
  )

  const { data: entries, loading: entLoading, error: entError, hasNext, hasPrev, currentPage, loadNext, loadPage, refresh: refreshEntries } =
    useCursorPagination(fetchEntries)

  if (accLoading || balLoading) return <LoadingCards count={2} />
  if (accError) return <ErrorState error={accError} onRetry={refetchAcc} />
  if (balError) return <ErrorState error={balError} />
  if (!account) return <EmptyState message="Account not found" />

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <Link to="/accounts">
          <Button variant="ghost" size="icon">
            <ArrowLeft className="h-5 w-5" />
          </Button>
        </Link>
        <div>
          <h1 className="text-2xl font-bold">{account.owner_ref || 'Account'}</h1>
          <p className="text-muted-foreground font-mono text-sm">{account.id}</p>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-4">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Posted</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {balLoading ? '...' : formatIDR(balance?.posted || 0)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Held</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {balLoading ? '...' : formatIDR(balance?.held || 0)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Available</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {balLoading ? '...' : formatIDR(balance?.available || 0)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Status</CardTitle>
          </CardHeader>
          <CardContent>
            <Badge variant={account.status === 'active' ? 'success' : 'secondary'} className="text-base">
              {account.status}
            </Badge>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Ledger Entries</CardTitle>
        </CardHeader>
        <CardContent>
          {entLoading && !entries ? (
            <LoadingState rows={5} />
          ) : entError ? (
            <ErrorState error={entError} onRetry={refreshEntries} />
          ) : !entries || entries.length === 0 ? (
            <EmptyState message="No ledger entries" />
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Date</TableHead>
                    <TableHead>Transfer</TableHead>
                    <TableHead>Direction</TableHead>
                    <TableHead className="text-right">Amount</TableHead>
                    <TableHead className="text-right">Balance After</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {entries.map(entry => (
                    <TableRow key={entry.id}>
                      <TableCell>{formatDate(entry.created_at)}</TableCell>
                      <TableCell>
                        <Link
                          to={`/transfers/${entry.transfer_id}`}
                          className="text-primary hover:underline font-mono text-sm"
                        >
                          {entry.transfer_id.slice(0, 8)}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant={entry.direction === 'credit' ? 'success' : 'secondary'}>
                          {entry.direction}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {formatIDR(entry.amount)}
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {formatIDR(entry.balance_after)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              <div className="flex items-center justify-between mt-4">
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
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
