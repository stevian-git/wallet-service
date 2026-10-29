import { useCallback, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { useCursorPagination } from '@/hooks/use-cursor'
import { LoadingState, EmptyState, ErrorState } from '@/components/states'
import { formatIDR, formatDate } from '@/lib/utils'
import { api } from '@/lib/api'
import type { ReconciliationRun, Discrepancy, PaginatedResponse } from '@/lib/types'
import { ChevronLeft, ChevronRight } from 'lucide-react'

function RunsList() {
  const fetchRuns = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams({ limit: '20' })
      if (cursor) params.set('cursor', cursor)
      return api.get<PaginatedResponse<ReconciliationRun>>(`/v1/reconciliation/runs?${params}`)
    },
    []
  )

  const { data, loading, error, hasNext, hasPrev, currentPage, loadNext, loadPage, refresh } =
    useCursorPagination(fetchRuns)

  if (error) return <ErrorState error={error} onRetry={refresh} />
  if (loading && !data) return <LoadingState rows={3} />
  if (!data || data.length === 0) return <EmptyState message="No reconciliation runs found" />

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Reconciliation Runs</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">Matched</TableHead>
                <TableHead className="text-right">Discrepancies</TableHead>
                <TableHead>Started</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map(run => (
                <TableRow key={run.id}>
                  <TableCell className="font-mono text-sm">{run.id.slice(0, 8)}</TableCell>
                  <TableCell>
                    <Badge variant={run.status === 'completed' ? 'success' : run.status === 'failed' ? 'destructive' : 'warning'}>
                      {run.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">{run.matched_count}</TableCell>
                  <TableCell className="text-right">{run.discrepancy_count}</TableCell>
                  <TableCell>{formatDate(run.started_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div className="flex items-center justify-between">
        <Button variant="outline" size="sm" onClick={() => loadPage(currentPage - 1)} disabled={!hasPrev} className="gap-1">
          <ChevronLeft className="h-4 w-4" />
          Previous
        </Button>
        <span className="text-sm text-muted-foreground">Page {currentPage + 1}</span>
        <Button variant="outline" size="sm" onClick={loadNext} disabled={!hasNext} className="gap-1">
          Next
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </>
  )
}

function DiscrepanciesTable({ runId }: { runId: string }) {
  const [resolution, setResolution] = useState<string>('all')

  const fetchDiscrepancies = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams({ limit: '20' })
      if (cursor) params.set('cursor', cursor)
      if (resolution !== 'all') params.set('resolution', resolution)
      return api.get<PaginatedResponse<Discrepancy>>(
        `/v1/reconciliation/runs/${runId}/discrepancies?${params}`
      )
    },
    [runId, resolution]
  )

  const { data, loading, error, hasNext, hasPrev, currentPage, loadNext, loadPage, refresh } =
    useCursorPagination(fetchDiscrepancies)

  const resolutionLabel: Record<string, string> = {
    missing_in_ledger: 'Missing in Ledger',
    missing_in_settlement: 'Missing in Settlement',
    amount_mismatch: 'Amount Mismatch',
    status_mismatch: 'Status Mismatch',
    duplicate_in_settlement: 'Duplicate',
  }

  return (
    <>
      <div className="flex items-center gap-4 mb-4">
        <h2 className="text-lg font-semibold">Discrepancies</h2>
        <Select value={resolution} onValueChange={setResolution}>
          <SelectTrigger className="w-48">
            <SelectValue placeholder="Filter by resolution" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All</SelectItem>
            <SelectItem value="open">Open</SelectItem>
            <SelectItem value="resolved">Resolved</SelectItem>
            <SelectItem value="ignored">Ignored</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {error ? (
        <ErrorState error={error} onRetry={refresh} />
      ) : loading && !data ? (
        <LoadingState rows={3} />
      ) : !data || data.length === 0 ? (
        <EmptyState message="No discrepancies found" />
      ) : (
        <>
          <Card>
            <CardContent className="pt-6">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Kind</TableHead>
                    <TableHead>Resolution</TableHead>
                    <TableHead>Transfer</TableHead>
                    <TableHead className="text-right">Expected</TableHead>
                    <TableHead className="text-right">Actual</TableHead>
                    <TableHead>Note</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.map(d => (
                    <TableRow key={d.id}>
                      <TableCell>
                        <Badge variant="outline">{resolutionLabel[d.kind] || d.kind}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant={
                            d.resolution === 'resolved' ? 'success' :
                            d.resolution === 'ignored' ? 'secondary' : 'warning'
                          }
                        >
                          {d.resolution}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {d.transfer_id ? (
                          <Link to={`/transfers/${d.transfer_id}`} className="text-primary hover:underline">
                            {d.transfer_id.slice(0, 8)}
                          </Link>
                        ) : '-'}
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {d.expected_amount != null ? formatIDR(d.expected_amount) : '-'}
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {d.actual_amount != null ? formatIDR(d.actual_amount) : '-'}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground max-w-xs truncate">
                        {d.note || '-'}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

          <div className="flex items-center justify-between mt-4">
            <Button variant="outline" size="sm" onClick={() => loadPage(currentPage - 1)} disabled={!hasPrev} className="gap-1">
              <ChevronLeft className="h-4 w-4" />
              Previous
            </Button>
            <span className="text-sm text-muted-foreground">Page {currentPage + 1}</span>
            <Button variant="outline" size="sm" onClick={loadNext} disabled={!hasNext} className="gap-1">
              Next
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </>
      )}
    </>
  )
}

export function ReconciliationPage() {
  const { runId } = useParams<{ runId: string }>()
  const { data: run, loading, error, refetch } = useApi<ReconciliationRun>(
    runId ? `/v1/reconciliation/runs/${runId}` : null
  )

  if (runId) {
    if (loading) return <LoadingState rows={3} />
    if (error) return <ErrorState error={error} onRetry={refetch} />
    if (!run) return <EmptyState message="Run not found" />

    return (
      <div className="space-y-6">
        <Link to="/reconciliation">
          <Button variant="outline" size="sm">Back to Runs</Button>
        </Link>

        <div className="grid gap-4 md:grid-cols-3">
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm text-muted-foreground">Status</CardTitle>
            </CardHeader>
            <CardContent>
              <Badge variant={run.status === 'completed' ? 'success' : run.status === 'failed' ? 'destructive' : 'warning'}>
                {run.status}
              </Badge>
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm text-muted-foreground">Matched</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-bold">{run.matched_count}</div>
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm text-muted-foreground">Discrepancies</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-bold text-destructive">{run.discrepancy_count}</div>
            </CardContent>
          </Card>
        </div>

        <DiscrepanciesTable runId={runId} />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Reconciliation</h1>
        <p className="text-muted-foreground">Settlement reconciliation runs</p>
      </div>
      <RunsList />
    </div>
  )
}
