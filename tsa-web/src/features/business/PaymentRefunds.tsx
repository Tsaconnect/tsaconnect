// A succeeded payment's refunds, in its drawer: what has been refunded, and asking for
// another. A live refund is only asked for here — the business owner approves and sends it
// from their TSA wallet in the TSA Connect app. A test one is simulated: done at once.
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink } from 'lucide-react';
import { dateTime, naira } from '@/lib/format';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { FormError } from '@/components/ui/form-error';
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { SectionHeading } from '@/components/ui/section-heading';
import { Skeleton } from '@/components/ui/skeleton';
import { RefundStatusPill } from '@/components/ui/status-pill';
import { toast } from '@/components/ui/toaster';
import { createRefund, listPaymentRefunds, type Mode, type Payment, type PaymentRefunds as RefundsAnswer, type Refund } from './api';
import { chainLabelById, txExplorerUrl } from './display';
import { queryKeys } from './queryKeys';
import { formatUsd, isOpenRefund, microOf, nairaForRefund, parseUsd, refundAmountError, waitingNote } from './refunds';
import { useIdempotencyKey } from './useIdempotencyKey';

function network(r: Refund): string {
  if (r.mode === 'test') return 'Simulated — nothing was sent';
  if (!r.chainId) return 'Network chosen when it is sent';
  return `${r.tokenSymbol ?? ''} on ${chainLabelById(r.chainId)}`.trim();
}

function RefundItem({ r }: { r: Refund }) {
  const url = r.txHash && r.chainId ? txExplorerUrl(r.txHash, r.chainId) : undefined;
  return (
    <li className="flex items-start gap-3 px-4 py-3">
      <RefundStatusPill status={r.status} />
      <div className="min-w-0 flex-1">
        <p className="font-semibold tabular">{r.amount} USD</p>
        <p className="text-caption text-text-secondary">{network(r)} · {dateTime(r.confirmedAt ?? r.createdAt)}</p>
        {r.reason && <p className="break-words text-caption text-text-secondary">{r.reason}</p>}
        {r.failureReason && <p className="mt-1 break-words text-caption text-danger-strong">{r.failureReason}</p>}
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer" className="mt-1 inline-flex items-center gap-1 text-caption font-semibold text-accent-strong hover:underline">
            View transaction<ExternalLink className="size-3.5" aria-hidden />
          </a>
        )}
      </div>
    </li>
  );
}

function RefundAction({ data, mode, onAsk }: { data: RefundsAnswer; mode: Mode; onAsk: () => void }) {
  // First: while a refund is open the API answers refundable:false ("A refund is already in
  // progress"), and the merchant needs to know what it waits for, not that generic refusal.
  const waiting = data.refunds.find((r) => isOpenRefund(r.status));
  if (waiting) return <Note tone="info">{waitingNote(waiting)}</Note>;
  if (!data.refundable) {
    return <p className="text-body-sm text-text-secondary">{data.notRefundableReason || "Refunds aren't available for this payment."}</p>;
  }
  if (microOf(data.remaining) === 0n) return <p className="text-body-sm text-text-secondary">This payment has been refunded in full.</p>;
  return (
    <Button variant="secondary" size="sm" className="justify-self-start" onClick={onAsk}>
      {mode === 'test' ? 'Simulate refund' : 'Refund'}
    </Button>
  );
}

function RefundDialog({ payment: p, mode, data, onClose }: { payment: Payment; mode: Mode; data: RefundsAnswer; onClose: () => void }) {
  const queryClient = useQueryClient();
  const idempotency = useIdempotencyKey();
  const remaining = microOf(data.remaining);
  const [amount, setAmount] = useState(formatUsd(remaining));
  const [reason, setReason] = useState('');
  const [tried, setTried] = useState(false);
  const amountError = refundAmountError(amount, remaining);
  const micro = amountError ? null : parseUsd(amount);
  const kobo = micro === null ? null : nairaForRefund(micro, p.customerTotal, microOf(data.cap));
  const test = mode === 'test';

  const create = useMutation({
    mutationFn: (body: { amount: string; reason: string }) => createRefund(mode, p.id, body, idempotency.keyFor(body)),
    onSuccess: ({ refund }) => {
      onClose();
      toast.success(refund.status === 'confirmed' ? 'Test refund done' : 'Refund requested — approve and send it in the TSA Connect app');
    },
    // Asked for or refused (over the cap, one already waiting), the refunds and the payment are read again.
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.paymentRefunds(mode, p.id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.payment(mode, p.id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.refunds(mode) });
    },
  });

  const submit = () => {
    setTried(true);
    if (micro === null) return;
    create.mutate({ amount: formatUsd(micro), reason: reason.trim() });
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title="Refund this payment"
      description={test
        ? 'Test mode: nothing is sent. The refund is done at once and a test payment.refunded webhook is sent.'
        : "Approve and send it in the TSA Connect app: it goes from your TSA wallet in USDT or USDC to the wallet your customer paid from. The fee they paid isn't returned."}
      focusFirstField
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button loading={create.isPending} onClick={submit}>{test ? 'Simulate refund' : 'Request refund'}</Button>
        </>
      }
    >
      <div className="grid gap-4">
        <Field
          label="Amount (USD)"
          error={(tried || amount.trim() !== '') && amountError ? amountError : undefined}
          hint={`Up to ${formatUsd(remaining)} USD${kobo !== null ? ` · about ${naira(kobo)} at this payment's rate` : ''}`}
        >
          <Input inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} />
        </Field>
        <Field label="Reason (optional)">
          <Input value={reason} maxLength={255} onChange={(e) => setReason(e.target.value)} />
        </Field>
        {create.error && <FormError>{create.error.message}</FormError>}
      </div>
    </Dialog>
  );
}

/** Shown for a succeeded payment only. */
export function PaymentRefunds({ payment: p, mode }: { payment: Payment; mode: Mode }) {
  const q = useQuery({ queryKey: queryKeys.paymentRefunds(mode, p.id), queryFn: ({ signal }) => listPaymentRefunds(mode, p.id, signal) });
  const [asking, setAsking] = useState(false);
  return (
    <>
      <SectionHeading>Refunds</SectionHeading>
      {q.data ? (
        <div className="grid gap-3">
          <RefundAction data={q.data} mode={mode} onAsk={() => setAsking(true)} />
          {q.data.refunds.length > 0 && (
            <ul className="divide-y divide-[var(--hairline)] rounded-md border border-hairline">
              {q.data.refunds.map((r) => <RefundItem key={r.id} r={r} />)}
            </ul>
          )}
        </div>
      ) : q.isPending ? (
        <Skeleton className="h-10 w-full" />
      ) : (
        <p role="alert" className="text-body-sm text-danger-strong">{q.error?.message}</p>
      )}
      {/* Mounted only while open: each opening starts from what is left now. */}
      {asking && q.data && <RefundDialog payment={p} mode={mode} data={q.data} onClose={() => setAsking(false)} />}
    </>
  );
}
