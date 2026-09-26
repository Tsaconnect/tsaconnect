import type { MouseEvent } from 'react';
import { Link, useNavigate, type To } from 'react-router-dom';
import { CreditCard } from 'lucide-react';
import { cn } from '@/lib/cn';
import { EmptyState } from '@/components/ui/states';
import { TBody, TD, TH, THead, TR, Table } from '@/components/ui/table';
import type { Mode, Payment } from './api';
import { paymentColumns, type PaymentColumn } from './paymentColumns';

/** Payments as rows; a row (or its link) opens the payment's detail. */
export function PaymentsTable({ items, loading, hrefFor, columns = paymentColumns }: {
  items: Payment[] | undefined;
  loading: boolean;
  hrefFor: (p: Payment) => To;
  columns?: PaymentColumn[];
}) {
  const navigate = useNavigate();
  const open = (e: MouseEvent, href: To) => {
    // The row's own link navigates itself; don't navigate twice.
    if ((e.target as HTMLElement).closest('a')) return;
    navigate(href);
  };
  return (
    <Table>
      <THead>
        <TR>
          {columns.map((c) => <TH key={c.header} className={cn(c.right && 'text-right')}>{c.header}</TH>)}
        </TR>
      </THead>
      <TBody>
        {loading && !items
          ? Array.from({ length: 5 }, (_, i) => (
              <TR key={i}>
                {columns.map((c) => <TD key={c.header}>{c.skeleton}</TD>)}
              </TR>
            ))
          : items?.map((p) => {
              const href = hrefFor(p);
              return (
                <TR key={p.id} className="cursor-pointer" onClick={(e) => open(e, href)}>
                  {columns.map((c) => <TD key={c.header} className={cn(c.right && 'text-right', c.className)}>{c.cell(p, href)}</TD>)}
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
