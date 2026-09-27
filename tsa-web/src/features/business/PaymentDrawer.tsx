import { useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError } from '@/lib/api';
import { cn } from '@/lib/cn';
import { dateTime, naira } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Drawer } from '@/components/ui/drawer';
import { FormError } from '@/components/ui/form-error';
import { SectionHeading } from '@/components/ui/section-heading';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import { StatusPill } from '@/components/ui/status-pill';
import { toast } from '@/components/ui/toaster';
import { cancelLink, getPayment, type Delivery, type Mode, type Payer, type Payment, type PaymentDetail } from './api';
import { LINK_STATUS_LABELS, linkMessage, paymentFor, statusOf, youReceive } from './display';
import { LinkShare } from './LinkShare';
import { queryKeys } from './queryKeys';
import { useMerchant } from './useMerchant';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-text-secondary">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

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

/** The link's own section: share it while it can be paid, and cancel it (confirmed first). */
function PaymentLink({ payment: p, mode }: { payment: Payment; mode: Mode }) {
  const businessName = useMerchant().data?.businessName ?? '';
  const [confirming, setConfirming] = useState(false);
  const queryClient = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => cancelLink(mode, p.id),
    // The answer is the cancelled payment: shown at once, so the drawer never flashes
    // back to waiting (with Cancel link) while it is read again.
    onSuccess: (cancelled) => {
      queryClient.setQueryData<PaymentDetail>(queryKeys.payment(mode, p.id), (old) => old && { ...old, payment: cancelled });
      setConfirming(false);
      toast.success('Link cancelled');
    },
    // Someone started paying (or paid) first: the fresh status says what happened.
    onError: (err) => {
      if (!(err instanceof ApiError && err.code === 'not_cancellable')) return;
      setConfirming(false);
      toast.error(err.message);
    },
    // Cancelled or not (it may have been paid meanwhile), the lists and this payment are read again.
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.payment(mode, p.id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.list('links', mode) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.list('payments', mode) });
    },
  });

  if (p.status !== 'pending') {
    return (
      <>
        <SectionHeading>Payment link</SectionHeading>
        <p className="font-mono text-[13px] break-all text-text-secondary">{p.checkoutUrl}</p>
      </>
    );
  }
  return (
    <>
      <SectionHeading>Payment link</SectionHeading>
      <div className="grid gap-3">
        <LinkShare url={p.checkoutUrl} message={linkMessage(businessName, p)} />
        <Button variant="danger" size="sm" className="justify-self-start" onClick={() => { cancel.reset(); setConfirming(true); }}>Cancel link</Button>
      </div>
      <Dialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Cancel this link?"
        description="The link will stop working. This can't be undone."
        footer={
          <>
            <Button variant="secondary" onClick={() => setConfirming(false)}>Keep link</Button>
            <Button variant="danger" loading={cancel.isPending} onClick={() => cancel.mutate()}>Cancel link</Button>
          </>
        }
      >
        {cancel.error ? <FormError>{cancel.error.message}</FormError> : <p className="text-body-sm">{naira(p.amount)} for {p.description}</p>}
      </Dialog>
    </>
  );
}

function Detail({ payment: p, payer, deliveries, mode, list }: { payment: Payment; payer: Payer | null; deliveries: Delivery[]; mode: Mode; list: 'payments' | 'links' }) {
  const forWhat = paymentFor(p);
  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <StatusPill status={statusOf(p)} labels={list === 'links' ? LINK_STATUS_LABELS : undefined} />
        <span className="min-w-0 truncate font-mono text-caption text-text-secondary" title={p.id}>{p.id}</span>
      </div>
      <p className="mt-2.5 text-title1 font-bold tabular">{naira(p.amount)}</p>

      <dl className="mt-5 grid grid-cols-[130px_minmax(0,1fr)] gap-x-4 gap-y-2.5 text-body-sm">
        {/* Only a succeeded payment was paid; before that it's whoever took it up. */}
        <Row label={p.status === 'succeeded' ? 'Paid by' : 'Payer'}><PaidBy payer={payer} status={p.status} /></Row>
        {p.customerName && <Row label="Requested for">{p.customerName}</Row>}
        <Row label="Your reference"><span className="font-mono text-[13px]">{p.reference}</span></Row>
        {forWhat.what && <Row label="For">{forWhat.what}</Row>}
        {forWhat.note && <Row label="Customer's note">{forWhat.note}</Row>}
        {p.source === 'open' && p.openLinkId && (
          <Row label="Paid through">
            <Link to={`/business/${mode}/links/open/${p.openLinkId}?type=reusable`} className="font-semibold text-accent-strong hover:underline">
              {p.linkLabel || 'Reusable link'}
            </Link>
          </Row>
        )}
        {/* While it waits to be paid, what it will pay — as the link's Created dialog says. */}
        {p.status === 'pending' ? (
          <Row label="You'll receive"><b className="tabular">{naira(p.merchantNet)}</b></Row>
        ) : (
          <Row label="You receive"><b className="tabular">{youReceive(p)}</b></Row>
        )}
        <Row label={p.status === 'succeeded' ? 'Customer paid' : 'Customer pays'}>
          <span className="tabular">{naira(p.customerTotal)}</span>{' '}
          <span className="text-text-secondary">(incl. {naira(p.fee)} fee)</span>
        </Row>
        {p.settlementRef && <Row label="Settlement ref"><span className="font-mono text-[13px]">{p.settlementRef}</span></Row>}
        <Row label="Created">{dateTime(p.createdAt)}</Row>
        {p.succeededAt ? (
          <Row label="Succeeded">{dateTime(p.succeededAt)}</Row>
        ) : (
          // A cancelled link closed when its owner cancelled it, not at this time.
          !p.cancelled && <Row label={p.status === 'expired' ? 'Expired' : 'Expires'}>{dateTime(p.expiresAt)}</Row>
        )}
        {p.failureReason && !p.cancelled && <Row label="Why it failed"><span className="text-danger-strong">{p.failureReason}</span></Row>}
      </dl>

      {p.source === 'link' && <PaymentLink payment={p} mode={mode} />}

      {/* Links are often made by owners with no webhook at all: nothing sent is nothing to show. */}
      {(p.source !== 'link' || deliveries.length > 0) && (
        <>
          <SectionHeading>Webhooks sent</SectionHeading>
          <Deliveries items={deliveries} />
        </>
      )}
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

/** One payment over its list (payments, or payment links); closing goes back to that list with its filters. */
export function PaymentDrawer({ list = 'payments' }: { list?: 'payments' | 'links' }) {
  const mode = useMode();
  const id = useParams().id ?? '';
  const navigate = useNavigate();
  const { search } = useLocation();
  const q = useQuery({ queryKey: queryKeys.payment(mode, id), queryFn: ({ signal }) => getPayment(mode, id, signal) });
  const notFound = q.error instanceof ApiError && q.error.status === 404;

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) navigate({ pathname: `/business/${mode}/${list}`, search }, { replace: true });
      }}
      title={q.data?.payment.reference ?? 'Payment'}
    >
      {q.data ? (
        <Detail payment={q.data.payment} payer={q.data.payer} deliveries={q.data.deliveries} mode={mode} list={list} />
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
