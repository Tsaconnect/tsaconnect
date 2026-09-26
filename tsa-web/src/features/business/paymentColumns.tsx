// The columns a payments table can show. Payments and payment links share the
// table (PaymentsTable) and differ only in these.
import type { ReactNode } from 'react';
import { Link, type To } from 'react-router-dom';
import { dateTime, naira, relative } from '@/lib/format';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { StatusPill } from '@/components/ui/status-pill';
import type { Payment } from './api';
import { LINK_STATUS_LABELS, statusOf, youReceive } from './display';

/**
 * One column. `cell` gets the row's detail link: one column renders it (rowLink),
 * so every row can also be opened from the keyboard.
 */
export type PaymentColumn = {
  header: string;
  right?: boolean;
  className?: string;
  cell: (p: Payment, href: To) => ReactNode;
  skeleton: ReactNode;
};

/** Text cut to one line inside a fixed width, so a long value never widens the table. */
const clipped = (width: string, text: string | undefined, content: ReactNode = text) => (
  <div className={`${width} truncate`} title={text}>{content}</div>
);

const rowLink = (to: To, children: ReactNode) => (
  <Link to={to} className="font-semibold text-text-primary hover:underline focus-visible:underline">{children}</Link>
);

const amount: PaymentColumn = {
  header: 'Amount',
  right: true,
  className: 'tabular whitespace-nowrap',
  cell: (p) => naira(p.amount),
  skeleton: <Skeleton className="ml-auto h-4 w-20" />,
};

const status: PaymentColumn = {
  header: 'Status',
  cell: (p) => <StatusPill status={statusOf(p)} />,
  skeleton: <Skeleton className="h-5 w-20 rounded-full" />,
};

const created: PaymentColumn = {
  header: 'Created',
  right: true,
  className: 'whitespace-nowrap text-caption text-text-secondary',
  cell: (p) => <time dateTime={p.createdAt} title={dateTime(p.createdAt)}>{relative(p.createdAt)}</time>,
  skeleton: <Skeleton className="ml-auto h-3 w-14" />,
};

export const paymentColumns: PaymentColumn[] = [
  {
    header: 'Reference',
    className: 'max-w-[320px]',
    cell: (p, href) => (
      <>
        <span className="flex items-center gap-2">
          {rowLink(href, p.reference)}
          {p.source === 'link' && <Badge tone="neutral">Link</Badge>}
        </span>
        {(p.customerName || p.description) && (
          <p className="max-w-[320px] truncate text-caption text-text-secondary">{[p.customerName, p.description].filter(Boolean).join(' · ')}</p>
        )}
      </>
    ),
    skeleton: <><Skeleton className="h-4 w-28" /><Skeleton className="mt-1.5 h-3 w-40" /></>,
  },
  amount,
  {
    header: 'You receive',
    right: true,
    className: 'tabular whitespace-nowrap',
    cell: youReceive,
    skeleton: <Skeleton className="ml-auto h-4 w-20" />,
  },
  status,
  created,
];

/** Until a link is paid or closed, when it stops working; after that, nothing to say. */
const closesAt = (p: Payment) => (p.status === 'pending' ? dateTime(p.expiresAt) : '—');

export const linkColumns: PaymentColumn[] = [
  {
    header: 'Customer',
    cell: (p) => (p.customerName ? clipped('max-w-[180px]', p.customerName) : <span className="text-text-secondary">—</span>),
    skeleton: <Skeleton className="h-4 w-24" />,
  },
  {
    header: 'What for',
    cell: (p, href) => clipped('max-w-[280px]', p.description, rowLink(href, p.description)),
    skeleton: <Skeleton className="h-4 w-40" />,
  },
  amount,
  { ...status, cell: (p) => <StatusPill status={statusOf(p)} labels={LINK_STATUS_LABELS} /> },
  created,
  {
    header: 'Expires',
    right: true,
    className: 'whitespace-nowrap text-caption text-text-secondary',
    cell: closesAt,
    skeleton: <Skeleton className="ml-auto h-3 w-20" />,
  },
];
