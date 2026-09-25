import type { ReactNode } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ApiError } from '@/lib/api';
import { cn } from '@/lib/cn';
import { dateTime, naira } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Drawer } from '@/components/ui/drawer';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import { StatusPill } from '@/components/ui/status-pill';
import { getPayment, type Delivery, type Payer, type Payment } from './api';
import { youReceive } from './display';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-text-secondary">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

const SectionHeading = ({ children }: { children: ReactNode }) => (
  <h3 className="mt-6 mb-2.5 text-overline font-semibold uppercase tracking-[.06em] text-text-secondary">{children}</h3>
);

function CodePill({ d }: { d: Delivery }) {
  const [label, cls] =
    d.attempts === 0 ? ['Queued', 'bg-surface-alt text-text-secondary']
    : d.lastStatusCode === 0 ? ['No response', 'bg-danger-bg text-danger-strong']
    : d.lastStatusCode >= 200 && d.lastStatusCode < 300 ? [String(d.lastStatusCode), 'bg-success-bg text-success-strong']
    : [String(d.lastStatusCode), 'bg-danger-bg text-danger-strong'];
  return <span className={cn('inline-flex shrink-0 items-center rounded-full px-2.5 py-0.5 text-caption font-semibold tabular', cls)}>{label}</span>;
}

function deliveryState(d: Delivery): string {
  const tries = `${d.attempts} ${d.attempts === 1 ? 'attempt' : 'attempts'}`;
  if (d.deliveredAt) return `Delivered ${dateTime(d.deliveredAt)} · ${tries}`;
  if (d.failedAt) return `Gave up ${dateTime(d.failedAt)} · ${tries}`;
  if (d.nextAttemptAt) return `Next try ${dateTime(d.nextAttemptAt)} · ${tries}`;
  return `${dateTime(d.createdAt)} · ${tries}`;
}

function Deliveries({ items }: { items: Delivery[] }) {
  if (items.length === 0) {
    return <p className="text-body-sm text-text-secondary">No webhooks sent for this payment yet.</p>;
  }
  return (
    <ul className="divide-y divide-[var(--hairline)] rounded-md border border-hairline">
      {items.map((d) => (
        <li key={d.id} className="flex items-start gap-3 px-4 py-3">
          <CodePill d={d} />
          <div className="min-w-0 flex-1">
            <p className="font-mono text-[13px] font-semibold">{d.event}</p>
            <p className="text-caption text-text-secondary">{deliveryState(d)}</p>
            {d.lastError && <p className="mt-1 break-words text-caption text-danger-strong">{d.lastError}</p>}
          </div>
        </li>
      ))}
    </ul>
  );
}

function PaidBy({ payer, status }: { payer: Payer | null; status: Payment['status'] }) {
  if (!payer) {
    return <span className="text-text-secondary">{status === 'pending' || status === 'expired' ? 'No one yet' : '—'}</span>;
  }
  return <b className="font-semibold">{payer.name}</b>;
}

function Detail({ payment: p, payer, deliveries }: { payment: Payment; payer: Payer | null; deliveries: Delivery[] }) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <StatusPill status={p.status} />
        <span className="min-w-0 truncate font-mono text-caption text-text-secondary" title={p.id}>{p.id}</span>
      </div>
      <p className="mt-2.5 text-title1 font-bold tabular">{naira(p.amount)}</p>

      <dl className="mt-5 grid grid-cols-[130px_minmax(0,1fr)] gap-x-4 gap-y-2.5 text-body-sm">
        <Row label="Paid by"><PaidBy payer={payer} status={p.status} /></Row>
        <Row label="Your reference"><span className="font-mono text-[13px]">{p.reference}</span></Row>
        {p.description && <Row label="For">{p.description}</Row>}
        <Row label="You receive"><b className="tabular">{youReceive(p)}</b></Row>
        <Row label="Customer paid">
          <span className="tabular">{naira(p.customerTotal)}</span>{' '}
          <span className="text-text-secondary">(incl. {naira(p.fee)} fee)</span>
        </Row>
        {p.settlementRef && <Row label="Settlement ref"><span className="font-mono text-[13px]">{p.settlementRef}</span></Row>}
        <Row label="Created">{dateTime(p.createdAt)}</Row>
        {p.succeededAt ? (
          <Row label="Succeeded">{dateTime(p.succeededAt)}</Row>
        ) : (
          <Row label={p.status === 'expired' ? 'Expired' : 'Expires'}>{dateTime(p.expiresAt)}</Row>
        )}
        {p.failureReason && <Row label="Why it failed"><span className="text-danger-strong">{p.failureReason}</span></Row>}
      </dl>

      <SectionHeading>Webhooks sent</SectionHeading>
      <Deliveries items={deliveries} />
    </>
  );
}

function DetailSkeleton() {
  return (
    <div className="grid gap-3" aria-hidden>
      <Skeleton className="h-5 w-40" />
      <Skeleton className="h-8 w-44" />
      {Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-4 w-full" />)}
    </div>
  );
}

/** One payment over the list; closing goes back to the list with its filters. */
export function PaymentDrawer() {
  const mode = useMode();
  const id = useParams().id ?? '';
  const navigate = useNavigate();
  const { search } = useLocation();
  const q = useQuery({ queryKey: ['payment', mode, id], queryFn: ({ signal }) => getPayment(mode, id, signal) });
  const notFound = q.error instanceof ApiError && q.error.status === 404;

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) navigate({ pathname: `/business/${mode}/payments`, search }, { replace: true });
      }}
      title={q.data?.payment.reference ?? 'Payment'}
    >
      {q.data ? (
        <Detail payment={q.data.payment} payer={q.data.payer} deliveries={q.data.deliveries} />
      ) : q.isPending ? (
        <DetailSkeleton />
      ) : notFound ? (
        <ErrorState title="Payment not found" body={`There's no ${mode} payment with this ID.`} />
      ) : (
        <ErrorState title="We couldn't load this payment" body={q.error?.message} onRetry={() => void q.refetch()} />
      )}
    </Drawer>
  );
}
