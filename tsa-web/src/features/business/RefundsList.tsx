// The refunds asked for in the Reconciliation page's period, by status. Each row opens its
// payment (its drawer has the refund's network and transaction). Status and page live in
// the URL (refundStatus, refundPage) beside the page's own period.
import { useEffect } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { dateTime } from '@/lib/format';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Segmented } from '@/components/ui/segmented';
import { Skeleton } from '@/components/ui/skeleton';
import { EmptyState, ErrorState } from '@/components/ui/states';
import { RefundStatusPill, type RefundStatus } from '@/components/ui/status-pill';
import { refundStatusLabel } from '@/components/ui/refund-status';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { listRefunds, type Mode } from './api';
import { chainLabelById, txExplorerUrl } from './display';
import { queryKeys } from './queryKeys';
import { refundAmountLabel } from './refunds';

/** What the reconciliation page passes: its period, `YYYY-MM-DD` (WAT, `to` inclusive). */
export type RefundsListProps = { mode: Mode; from: string; to: string };

const STATUSES: readonly { value: '' | RefundStatus; label: string }[] = [
  { value: '', label: 'All' },
  ...(['requested', 'submitted', 'confirmed', 'needs_review', 'failed', 'cancelled'] as const).map((s) => ({ value: s, label: refundStatusLabel(s) })),
];

export function RefundsList({ mode, from, to }: RefundsListProps) {
  const [params, setParams] = useSearchParams();
  const status = STATUSES.find((s) => s.value === params.get('refundStatus'))?.value ?? '';
  const page = Math.max(1, Math.floor(Number(params.get('refundPage'))) || 1);
  const filter = { status, from, to, page };
  const q = useQuery({ queryKey: queryKeys.refunds(mode, filter), queryFn: ({ signal }) => listRefunds(mode, filter, signal) });

  const update = (next: Record<string, string>) => {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) {
      if (v) p.set(k, v);
      else p.delete(k);
    }
    setParams(p);
  };

  const data = q.data;
  // A period change can leave the page past the last one: start over instead of showing an empty page.
  const pastEnd = !!data && data.total > 0 && data.refunds.length === 0 && page > 1;
  useEffect(() => {
    if (pastEnd) {
      setParams((prev) => {
        const p = new URLSearchParams(prev);
        p.delete('refundPage');
        return p;
      }, { replace: true });
    }
  }, [pastEnd, setParams]);
  const first = data && data.refunds.length ? (data.page - 1) * data.limit + 1 : 0;
  const last = data && data.refunds.length ? first + data.refunds.length - 1 : 0;

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-3 border-b border-hairline px-5 py-3">
        <h2 className="mr-auto text-title3 font-semibold">Refunds</h2>
        <Segmented
          look="chips"
          legend="Refund status"
          name="refundStatus"
          value={status}
          options={STATUSES}
          onChange={(value) => update({ refundStatus: value, refundPage: '' })}
        />
      </div>
      {q.isError && !data ? (
        <ErrorState title="We couldn't load your refunds" body={q.error.message} onRetry={() => void q.refetch()} />
      ) : !data || pastEnd ? (
        <div className="grid gap-2 p-5" aria-hidden>
          {Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="h-8 w-full" />)}
        </div>
      ) : data.total === 0 ? (
        <EmptyState title="No refunds in this period" body={status ? 'Try another status.' : 'Refunds you ask for from a payment appear here.'} />
      ) : (
        <>
          <Table>
            <THead>
              <TR>
                <TH>Asked</TH>
                <TH>Payment</TH>
                <TH>Customer</TH>
                <TH className="text-right">Amount</TH>
                <TH>Network</TH>
                <TH>Status</TH>
              </TR>
            </THead>
            <TBody>
              {data.refunds.map((r) => {
                const txUrl = r.txHash && r.chainId ? txExplorerUrl(r.txHash, r.chainId) : undefined;
                return (
                <TR key={r.id}>
                  <TD className="whitespace-nowrap text-text-secondary">{dateTime(r.createdAt)}</TD>
                  <TD>
                    <Link to={`/business/${mode}/payments/${r.paymentId}`} className="font-mono text-[13px] font-semibold text-accent-strong hover:underline">
                      {r.paymentCode || r.paymentId.slice(0, 8)}
                    </Link>
                    {r.description && <p className="max-w-[240px] truncate text-caption text-text-secondary">{r.description}</p>}
                  </TD>
                  <TD>{r.customerName || '—'}</TD>
                  <TD className="whitespace-nowrap text-right font-semibold tabular">{refundAmountLabel(r)}</TD>
                  <TD className="whitespace-nowrap">{r.chainId ? `${r.tokenSymbol ?? ''} on ${chainLabelById(r.chainId)}`.trim() : '—'}</TD>
                  <TD>
                    <RefundStatusPill status={r.status} />
                    {r.lastBroadcastError && <p className="mt-1 max-w-[220px] text-caption text-danger-strong">{r.lastBroadcastError}</p>}
                    {txUrl && (
                      <a href={txUrl} target="_blank" rel="noopener noreferrer" className="mt-1 block text-caption font-semibold text-accent-strong hover:underline">
                        View transaction
                      </a>
                    )}
                  </TD>
                </TR>
                );
              })}
            </TBody>
          </Table>
          <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-5 py-3">
            <p className="flex-1 text-caption text-text-secondary tabular">
              Showing {first}–{last} of {data.total}
            </p>
            <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => update({ refundPage: page > 2 ? String(page - 1) : '' })}>
              <ChevronLeft className="size-4" aria-hidden />Prev
            </Button>
            <Button variant="secondary" size="sm" disabled={last >= data.total} onClick={() => update({ refundPage: String(page + 1) })}>
              Next<ChevronRight className="size-4" aria-hidden />
            </Button>
          </div>
        </>
      )}
    </Card>
  );
}
