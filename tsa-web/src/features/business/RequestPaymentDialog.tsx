import { useId, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Clock, FlaskConical } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { naira, parseNaira } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { FormError } from '@/components/ui/form-error';
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { Segmented } from '@/components/ui/segmented';
import { createLink, type LinkDays, type Mode, type NewLink, type OwnMerchant, type Payment } from './api';
import { LinkShare } from './LinkShare';

const DAYS = [
  { value: '1', label: '1 day' },
  { value: '3', label: '3 days' },
  { value: '7', label: '7 days' },
] as const;

type Draft = { amount: string; description: string; customerName: string; reference: string; days: (typeof DAYS)[number]['value'] };
type Errors = Partial<Record<'amount' | 'description' | 'reference', string>>;

function check(d: Draft): Errors {
  const errors: Errors = {};
  if (parseNaira(d.amount) === null) errors.amount = 'Enter an amount in naira, like 5000 or 5000.50.';
  if (!d.description.trim()) errors.description = 'Say what the payment is for — your customer sees this.';
  return errors;
}

/**
 * "Request payment": a form for a payment link, then the link to share. Mounted
 * fresh on every open and on a mode change (Back, say): the API's Idempotency-Key
 * ignores ?mode, so a live key reused in test would replay the live link.
 */
export function RequestPaymentDialog({ open, onOpenChange, merchant }: { open: boolean; onOpenChange: (open: boolean) => void; merchant: OwnMerchant }) {
  const mode = useMode();
  return open ? <Flow key={mode} onOpenChange={onOpenChange} merchant={merchant} /> : null;
}

function Flow({ onOpenChange, merchant }: { onOpenChange: (open: boolean) => void; merchant: OwnMerchant }) {
  const mode = useMode();
  const queryClient = useQueryClient();
  const formId = useId();
  // One Idempotency-Key per request body: sending the same form again (a reply
  // lost on the way) replays the link already made; any edit is a new request, so
  // it never meets the API's refusal of a key reused with a different body.
  const sent = useRef<{ body: string; key: string } | null>(null);
  const keyFor = (link: NewLink) => {
    const body = JSON.stringify(link);
    if (sent.current?.body !== body) sent.current = { body, key: crypto.randomUUID() };
    return sent.current.key;
  };
  const [draft, setDraft] = useState<Draft>({ amount: '', description: '', customerName: '', reference: '', days: '7' });
  const [errors, setErrors] = useState<Errors>({});
  const close = () => onOpenChange(false);

  const create = useMutation({
    mutationFn: (link: NewLink) => createLink(mode, link, keyFor(link)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['links', mode] });
      void queryClient.invalidateQueries({ queryKey: ['payments', mode] });
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'reference_taken') setErrors({ reference: err.message });
      // The key was already used for something else: start a new one, and let the
      // list behind show whether the first try made a link after all.
      if (isKeyConflict(err)) {
        sent.current = null;
        void queryClient.invalidateQueries({ queryKey: ['links', mode] });
      }
    },
  });

  const set = (field: keyof Draft) => (value: string) => setDraft((d) => ({ ...d, [field]: value }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = check(draft);
    setErrors(found);
    if (Object.keys(found).length || create.isPending) return;
    create.mutate({
      amount: parseNaira(draft.amount)!,
      description: draft.description.trim(),
      customerName: draft.customerName.trim(),
      reference: draft.reference.trim(),
      expiresInDays: Number(draft.days) as LinkDays,
    });
  };

  const created = create.data;
  // Live is switched off for this business: say so before the form, not after it.
  const liveOff = mode === 'live' && !(merchant.liveAllowed ?? merchant.liveEnabled);
  const refusal = create.error && !(create.error instanceof ApiError && create.error.code === 'reference_taken') ? create.error : null;
  const footer = created ? (
    <Button onClick={close}>Done</Button>
  ) : liveOff ? (
    <Button variant="secondary" onClick={close}>Cancel</Button>
  ) : (
    <>
      <Button variant="secondary" onClick={close}>Cancel</Button>
      <Button type="submit" form={formId} loading={create.isPending}>Create link</Button>
    </>
  );

  return (
    <Dialog open onOpenChange={onOpenChange} title={created ? 'Your payment link is ready' : 'Request payment'} footer={footer}>
      {created ? (
        <Created payment={created} businessName={merchant.businessName} />
      ) : liveOff ? (
        <Note tone="info" icon={<Clock />}>
          Live payments aren't on for your business yet — make a test link.{' '}
          <Link to="/business/test/links" onClick={close} className="font-semibold underline underline-offset-2">Switch to test mode</Link>
        </Note>
      ) : (
        <form id={formId} onSubmit={submit} noValidate className="grid gap-4">
          {mode === 'test' && <Note tone="gold" icon={<FlaskConical />}>This is a test link — no real money moves.</Note>}
          {refusal && <Refusal error={refusal} mode={mode} onLeave={close} />}
          <Field label="Amount (₦)" error={errors.amount}>
            <Input inputMode="decimal" placeholder="5,000" value={draft.amount} onChange={(e) => set('amount')(e.target.value)} autoFocus />
          </Field>
          <Field label="What it's for" hint="Your customer sees this when they pay." error={errors.description}>
            <Input maxLength={255} placeholder="Balance for LOS–ABV, 12 Oct" value={draft.description} onChange={(e) => set('description')(e.target.value)} />
          </Field>
          <Field label="Customer name (optional)" hint="Your customer sees who the payment is for.">
            <Input maxLength={100} autoComplete="off" value={draft.customerName} onChange={(e) => set('customerName')(e.target.value)} />
          </Field>
          <Field label="Your reference (optional)" hint="Leave blank and we'll make one." error={errors.reference}>
            <Input maxLength={128} autoComplete="off" value={draft.reference} onChange={(e) => set('reference')(e.target.value)} />
          </Field>
          <div className="grid gap-1.5">
            <p aria-hidden className="text-[13px] leading-[18px] font-semibold text-text-secondary">Link open for</p>
            <Segmented legend="Link open for" name="days" value={draft.days} options={DAYS} onChange={set('days')} />
          </div>
        </form>
      )}
    </Dialog>
  );
}

/** The Idempotency middleware's refusal: an uncoded 409 (a coded one is the handler's own). */
const isKeyConflict = (err: unknown) => err instanceof ApiError && err.status === 409 && !err.code;

/** Why the API refused, with the way forward when there is one. */
function Refusal({ error, mode, onLeave }: { error: Error; mode: Mode; onLeave: () => void }) {
  const code = error instanceof ApiError ? error.code : undefined;
  const link = 'font-semibold underline underline-offset-2';
  return (
    <FormError>
      <p>
        {isKeyConflict(error) ? 'This link may already have been made — check your payment links.' : error.message}
        {code === 'live_not_enabled' && <> <Link to="/business/test/links" onClick={onLeave} className={link}>Switch to test mode</Link></>}
        {code === 'settlement_not_set' && <> <Link to={`/business/${mode}/payouts`} onClick={onLeave} className={link}>Set up payouts</Link></>}
      </p>
    </FormError>
  );
}

function Created({ payment: p, businessName }: { payment: Payment; businessName: string }) {
  return (
    <div className="grid gap-4">
      <div>
        <p className="text-title1 font-bold tabular">{naira(p.amount)}</p>
        <p className="text-body-sm text-text-secondary">{p.description}</p>
        <dl className="mt-2.5 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-body-sm">
          {/* The fee is added on top, so the customer's total can be more than the amount asked. */}
          {p.customerTotal !== p.amount && (
            <>
              <dt className="text-text-secondary">Your customer pays</dt>
              <dd className="font-semibold tabular">{naira(p.customerTotal)}</dd>
            </>
          )}
          <dt className="text-text-secondary">You'll receive</dt>
          <dd className="font-semibold tabular">{naira(p.merchantNet)}</dd>
        </dl>
      </div>
      <LinkShare businessName={businessName} payment={p} />
    </div>
  );
}
