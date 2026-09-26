import type { MouseEvent } from 'react';
import { Link, useNavigate, type To } from 'react-router-dom';
import { CreditCard } from 'lucide-react';
import { dateTime, naira, relative } from '@/lib/format';
import { Skeleton } from '@/components/ui/skeleton';
import { EmptyState } from '@/components/ui/states';
import { StatusPill } from '@/components/ui/status-pill';
import { TBody, TD, TH, THead, TR, Table } from '@/components/ui/table';
import type { Mode, Payment } from './api';
import { youReceive } from './display';

/** Payments as rows; a row (or its reference) opens the payment's detail. */
export function PaymentsTable({ items, loading, hrefFor }: { items: Payment[] | undefined; loading: boolean; hrefFor: (p: Payment) => To }) {
  const navigate = useNavigate();
  const open = (e: MouseEvent, href: To) => {
    // The reference is itself a link; don't navigate twice.
    if ((e.target as HTMLElement).closest('a')) return;
    navigate(href);
  };
  return (
    <Table>
      <THead>
        <TR>
          <TH>Reference</TH>
          <TH className="text-right">Amount</TH>
          <TH className="text-right">You receive</TH>
          <TH>Status</TH>
          <TH className="text-right">Created</TH>
        </TR>
      </THead>
      <TBody>
        {loading && !items
          ? Array.from({ length: 5 }, (_, i) => (
              <TR key={i}>
                <TD><Skeleton className="h-4 w-28" /><Skeleton className="mt-1.5 h-3 w-40" /></TD>
                <TD><Skeleton className="ml-auto h-4 w-20" /></TD>
                <TD><Skeleton className="ml-auto h-4 w-20" /></TD>
                <TD><Skeleton className="h-5 w-20 rounded-full" /></TD>
                <TD><Skeleton className="ml-auto h-3 w-14" /></TD>
              </TR>
            ))
          : items?.map((p) => {
              const href = hrefFor(p);
              return (
                <TR key={p.id} className="cursor-pointer" onClick={(e) => open(e, href)}>
                  <TD className="max-w-[320px]">
                    <Link to={href} className="font-semibold text-text-primary hover:underline focus-visible:underline">{p.reference}</Link>
                    {p.description && <p className="truncate text-caption text-text-secondary">{p.description}</p>}
                  </TD>
                  <TD className="text-right tabular whitespace-nowrap">{naira(p.amount)}</TD>
                  <TD className="text-right tabular whitespace-nowrap">{youReceive(p)}</TD>
                  <TD><StatusPill status={p.status} /></TD>
                  <TD className="text-right whitespace-nowrap text-caption text-text-secondary">
                    <time dateTime={p.createdAt} title={dateTime(p.createdAt)}>{relative(p.createdAt)}</time>
                  </TD>
                </TR>
              );
            })}
      </TBody>
    </Table>
  );
}

/** The first-run empty list: test mode points to the docs. */
export function NoPaymentsYet({ mode }: { mode: Mode }) {
  return (
    <EmptyState
      icon={<CreditCard className="size-5" aria-hidden />}
      title="No payments yet"
      body={
        mode === 'test' ? (
          <>Create a test payment with your test key — <Link to="/docs" className="font-semibold text-accent-strong underline-offset-2 hover:underline">see the docs</Link>.</>
        ) : (
          'When customers pay you, their payments appear here.'
        )
      }
    />
  );
}
