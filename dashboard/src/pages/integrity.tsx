import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { LoadingCards, ErrorState } from '@/components/states'
import { formatDate } from '@/lib/utils'
import { RefreshCw } from 'lucide-react'
import type { IntegrityReport } from '@/lib/types'
import { Button } from '@/components/ui/button'

export function IntegrityPage() {
  const { data, loading, error, refetch } = useApi<IntegrityReport>('/v1/admin/integrity')

  if (loading) return <LoadingCards count={3} />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  if (!data) return null

  const allOk = data.ok

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Integrity Check</h1>
          <p className="text-muted-foreground">Ledger invariant verification</p>
        </div>
        <Button onClick={refetch} variant="outline" size="sm" className="gap-2">
          <RefreshCw className="h-4 w-4" />
          Re-check
        </Button>
      </div>

      <Card className={allOk ? 'border-green-500' : 'border-destructive'}>
        <CardHeader>
          <div className="flex items-center gap-3">
            <CardTitle className="text-lg">Overall Status</CardTitle>
            <Badge variant={allOk ? 'success' : 'destructive'} className="text-base px-3 py-1">
              {allOk ? 'ALL OK' : 'ISSUES FOUND'}
            </Badge>
          </div>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Checked at {formatDate(data.checked_at)}
          </p>
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Balance Drift</CardTitle>
          </CardHeader>
          <CardContent>
            <div className={`text-2xl font-bold ${data.balance_drift.length > 0 ? 'text-destructive' : 'text-green-600'}`}>
              {data.balance_drift.length}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Unbalanced Transfers</CardTitle>
          </CardHeader>
          <CardContent>
            <div className={`text-2xl font-bold ${data.unbalanced_transfers.length > 0 ? 'text-destructive' : 'text-green-600'}`}>
              {data.unbalanced_transfers.length}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Posted Without Entries</CardTitle>
          </CardHeader>
          <CardContent>
            <div className={`text-2xl font-bold ${data.posted_without_entries.length > 0 ? 'text-destructive' : 'text-green-600'}`}>
              {data.posted_without_entries.length}
            </div>
          </CardContent>
        </Card>
      </div>

      {data.balance_drift.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Balance Drift Detected</CardTitle>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Account ID</TableHead>
                  <TableHead className="text-right">Cached</TableHead>
                  <TableHead className="text-right">Derived</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.balance_drift.map(d => (
                  <TableRow key={d.account_id}>
                    <TableCell className="font-mono text-sm">{d.account_id}</TableCell>
                    <TableCell className="text-right font-mono">{d.cached_posted}</TableCell>
                    <TableCell className="text-right font-mono">{d.derived_posted}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {data.unbalanced_transfers.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Unbalanced Transfers</CardTitle>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Transfer ID</TableHead>
                  <TableHead className="text-right">Total Debit</TableHead>
                  <TableHead className="text-right">Total Credit</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.unbalanced_transfers.map(t => (
                  <TableRow key={t.transfer_id}>
                    <TableCell className="font-mono text-sm">{t.transfer_id}</TableCell>
                    <TableCell className="text-right font-mono">{t.total_debit}</TableCell>
                    <TableCell className="text-right font-mono">{t.total_credit}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {data.posted_without_entries.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Posted Transfers Without Entries</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1">
              {data.posted_without_entries.map(id => (
                <li key={id} className="font-mono text-sm">{id}</li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
