import { useParams, Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { LoadingCards, EmptyState, ErrorState } from '@/components/states'
import { formatIDR, formatDate } from '@/lib/utils'
import type { TransferDetail } from '@/lib/types'
import { ArrowLeft } from 'lucide-react'

export function TransferDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data, loading, error, refetch } = useApi<TransferDetail>(
    id ? `/v1/transfers/${id}` : null
  )

  if (loading) return <LoadingCards count={2} />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  if (!data) return <EmptyState message="Transfer not found" />

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <Link to="/transfers">
          <Button variant="ghost" size="icon">
            <ArrowLeft className="h-5 w-5" />
          </Button>
        </Link>
        <div>
          <h1 className="text-2xl font-bold">Transfer</h1>
          <p className="text-muted-foreground font-mono text-sm">{data.id}</p>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-4">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Type</CardTitle>
          </CardHeader>
          <CardContent>
            <Badge>{data.type}</Badge>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Status</CardTitle>
          </CardHeader>
          <CardContent>
            <Badge variant={data.status === 'posted' ? 'success' : 'secondary'}>
              {data.status}
            </Badge>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Amount</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-xl font-bold">{formatIDR(data.amount)}</div>
            <div className="text-xs text-muted-foreground">{data.currency}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">Created</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-sm">{formatDate(data.created_at)}</div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Details</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-2 gap-2">
            <div className="text-sm text-muted-foreground">Source Account</div>
            <div className="font-mono text-sm">
              {data.source_account_id ? (
                <Link
                  to={`/accounts/${data.source_account_id}`}
                  className="text-primary hover:underline"
                >
                  {data.source_account_id}
                </Link>
              ) : (
                '-'
              )}
            </div>
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div className="text-sm text-muted-foreground">Destination Account</div>
            <div className="font-mono text-sm">
              {data.destination_account_id ? (
                <Link
                  to={`/accounts/${data.destination_account_id}`}
                  className="text-primary hover:underline"
                >
                  {data.destination_account_id}
                </Link>
              ) : (
                '-'
              )}
            </div>
          </div>
          {data.reference && (
            <div className="grid grid-cols-2 gap-2">
              <div className="text-sm text-muted-foreground">Reference</div>
              <div className="text-sm">{data.reference}</div>
            </div>
          )}
          {data.reverses_transfer_id && (
            <div className="grid grid-cols-2 gap-2">
              <div className="text-sm text-muted-foreground">Reverses</div>
              <div className="font-mono text-sm">
                <Link
                  to={`/transfers/${data.reverses_transfer_id}`}
                  className="text-primary hover:underline"
                >
                  {data.reverses_transfer_id}
                </Link>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Ledger Entries</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Account</TableHead>
                <TableHead>Direction</TableHead>
                <TableHead className="text-right">Amount</TableHead>
                <TableHead className="text-right">Balance After</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.entries.map(entry => (
                <TableRow key={entry.id}>
                  <TableCell>
                    <Link
                      to={`/accounts/${entry.account_id}`}
                      className="text-primary hover:underline font-mono text-sm"
                    >
                      {entry.account_id}
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
        </CardContent>
      </Card>
    </div>
  )
}
