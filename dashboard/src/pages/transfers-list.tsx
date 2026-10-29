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
import { formatIDR, formatShortDate } from '@/lib/utils'
import { api } from '@/lib/api'
import type { Transfer, PaginatedResponse } from '@/lib/types'
import { ChevronLeft, ChevronRight } from 'lucide-react'

const typeColors: Record<string, string> = {
  topup: 'bg-blue-500',
  transfer: 'bg-purple-500',
  withdrawal: 'bg-orange-500',
  hold_capture: 'bg-teal-500',
  reversal: 'bg-red-500',
}

export function TransfersListPage() {
  const fetchTransfers = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams({ limit: '20' })
      if (cursor) params.set('cursor', cursor)
      return api.get<PaginatedResponse<Transfer>>(`/v1/transfers?${params}`)
    },
    []
  )

  const { data, loading, error, hasNext, hasPrev, currentPage, loadNext, loadPage, refresh } =
    useCursorPagination(fetchTransfers)

  if (error) return <ErrorState error={error} onRetry={refresh} />
  if (loading && !data) return <LoadingState rows={5} />
  if (!data || data.length === 0) return <EmptyState message="No transfers found" />

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Transfers</h1>
        <p className="text-muted-foreground">All ledger transfers</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Transfer List</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">Amount</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Destination</TableHead>
                <TableHead>Date</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map(transfer => (
                <TableRow key={transfer.id}>
                  <TableCell>
                    <Link
                      to={`/transfers/${transfer.id}`}
                      className="text-primary hover:underline font-mono text-sm"
                    >
                      {transfer.id.slice(0, 8)}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <span className={`inline-flex items-center rounded px-2 py-0.5 text-xs text-white ${typeColors[transfer.type] || 'bg-gray-500'}`}>
                      {transfer.type}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Badge variant={transfer.status === 'posted' ? 'success' : 'secondary'}>
                      {transfer.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono">
                    {formatIDR(transfer.amount)}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {transfer.source_account_id?.slice(0, 8) || '-'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {transfer.destination_account_id?.slice(0, 8) || '-'}
                  </TableCell>
                  <TableCell>{formatShortDate(transfer.created_at)}</TableCell>
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
