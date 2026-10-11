import { useId, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { FlaskConical, Printer } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { naira, parseNaira } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { FormError } from '@/components/ui/form-error';
import { inlineLink } from '@/components/ui/inline-link';
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { Segmented } from '@/components/ui/segmented';
import {
  createLink, createOpenLink,
  type LinkDays, type Mode, type NewLink, type NewOpenLink, type OpenLink, type OwnMerchant, type Payment,
} from './api';
import { isLiveOff, linkCard, linkMessage, openAmountRule, openLinkCard, openLinkMessage } from './display';
import { LinkShare } from './LinkShare';
import { LiveOffNote, TestModeLink } from './LiveOffNote';
import { OpenLinkFields } from './OpenLinkFields';
import { AMOUNT_HINT, checkOpenLink, emptyOpenLinkDraft, toOpenLink, type OpenLinkDraft, type OpenLinkErrors } from './openLinkForm';
import { queryKeys, type ListKind } from './queryKeys';
import { useIdempotencyKey } from './useIdempotencyKey';

// '0': the link never expires (no expiresInDays is sent).
const DAYS = [
  { value: '0', label: 'No expiry' },
  { value: '1', label: '1 day' },
  { value: '3', label: '3 days' },
  { value: '7', label: '7 days' },
] as const;

// once: a fixed amount for one customer (a payment link). open: one reusable
// link many customers pay, each entering the amount (a shop's QR).
export type LinkKind = 'once' | 'open';

const KINDS: readonly { value: LinkKind; label: ReactNode }[] = [
  { value: 'once', label: <><span className="block">Fixed amount</span><span className="block font-normal">For one customer</span></> },
  { value: 'open', label: <><span className="block">Customer enters the amount</span><span className="block font-normal">Reusable, good for a shop QR</span></> },
];

type OnceDraft = { amount: string; description: string; customerName: string; reference: string; days: (typeof DAYS)[number]['value'] };
type OnceErrors = Partial<Record<'amount' | 'description' | 'reference', string>>;

function checkOnce(d: OnceDraft): OnceErrors {
  const errors: OnceErrors = {};
  if (parseNaira(d.amount) === null) errors.amount = AMOUNT_HINT;
  if (!d.description.trim()) errors.description = 'Say what the payment is for — your customer sees this.';
  return errors;
}

/** A form's fields as state, with a setter per field typed to that field. */
function useDraft<D extends object>(initial: D) {
  const [draft, setDraft] = useState(initial);
  const set = <K extends keyof D>(field: K) => (value: D[K]) => setDraft((d) => ({ ...d, [field]: value }));
  return { draft, set };
}

/** What one type of link contributes to the dialog. */
type Flow = { title: string; body: ReactNode; footer: ReactNode };
type FlowProps = { mode: Mode; merchant: OwnMerchant; close: () => void; picker: ReactNode };

/**
 * A create request for one type of link: its Idempotency-Key follows the body;
 * the lists it lands in are refreshed.
 */
function useCreate<Body, Made>(send: (body: Body, key: string) => Promise<Made>, lists: ListKind[], mode: Mode) {
  const queryClient = useQueryClient();
  const idem = useIdempotencyKey();
  const refresh = () => lists.forEach((kind) => void queryClient.invalidateQueries({ queryKey: queryKeys.list(kind, mode) }));
  return useMutation({
    mutationFn: (body: Body) => send(body, idem.keyFor(body)),
    onSuccess: refresh,
    onError: (err) => {
      // The key was already used for another body: start a new one, and let the
      // list behind show whether the first try made a link after all.
      if (isKeyConflict(err)) {
        idem.forget();
        refresh();
      }
    },
  });
}

/**
 * "Request payment": the choice of link, its form, then the link to share. Mounted
 * fresh on every open and on a mode change (Back, say): the API's Idempotency-Key
 * ignores ?mode, so a live key reused in test would replay the live link.
 */
export function RequestPaymentDialog({ open, onOpenChange, merchant, kind = 'once' }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  merchant: OwnMerchant;
  /** The type chosen when the form opens (the one the list is showing). */
  kind?: LinkKind;
}) {
  const mode = useMode();
  return open ? <Shell key={mode} onOpenChange={onOpenChange} merchant={merchant} initialKind={kind} /> : null;
}

function Shell({ onOpenChange, merchant, initialKind }: { onOpenChange: (open: boolean) => void; merchant: OwnMerchant; initialKind: LinkKind }) {
  const mode = useMode();
  const [kind, setKind] = useState(initialKind);
  const close = () => onOpenChange(false);
  const picker = <Segmented legend="Type of link" name="kind" value={kind} options={KINDS} onChange={setKind} />;
  // Both types keep their state, so switching back finds what was typed.
  const flows: Record<LinkKind, Flow> = {
    once: useOnceFlow({ mode, merchant, close, picker }),
    open: useOpenFlow({ mode, merchant, close, picker }),
  };
  // Live is switched off for this business: say so before the form, not after it.
  const flow: Flow = isLiveOff(merchant, mode)
    ? {
        title: 'Request payment',
        body: <LiveOffNote doInstead="make a test link" reusable={kind === 'open'} onLeave={close} />,
        footer: <Button variant="secondary" onClick={close}>Cancel</Button>,
      }
    : flows[kind];
  return (
    <Dialog open onOpenChange={onOpenChange} title={flow.title} footer={flow.footer} focusFirstField>
      {flow.body}
    </Dialog>
  );
}

/** The top of either form: the test-mode reminder and the API's refusal, if any. */
function FormTop({ mode, error, reusable, close }: { mode: Mode; error: Error | null; reusable: boolean; close: () => void }) {
  return (
    <>
      {mode === 'test' && <Note tone="gold" icon={<FlaskConical />}>This is a test link — no real money moves.</Note>}
      {error && <Refusal error={error} mode={mode} reusable={reusable} onLeave={close} />}
    </>
  );
}

const cancelAndSubmit = (close: () => void, formId: string, busy: boolean, label: string) => (
  <>
    <Button variant="secondary" onClick={close}>Cancel</Button>
    <Button type="submit" form={formId} loading={busy}>{label}</Button>
  </>
);

/** A fixed amount for one customer: a payment link. */
function useOnceFlow({ mode, merchant, close, picker }: FlowProps): Flow {
  const formId = useId();
  const { draft, set } = useDraft<OnceDraft>({ amount: '', description: '', customerName: '', reference: '', days: '0' });
  const [errors, setErrors] = useState<OnceErrors>({});
  const create = useCreate((link: NewLink, key) => createLink(mode, link, key), ['links', 'payments'], mode);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = checkOnce(draft);
    setErrors(found);
    if (Object.keys(found).length || create.isPending) return;
    create.mutate({
      amount: parseNaira(draft.amount)!,
      description: draft.description.trim(),
      customerName: draft.customerName.trim(),
      reference: draft.reference.trim(),
      ...(draft.days !== '0' ? { expiresInDays: Number(draft.days) as LinkDays } : {}),
    }, {
      onError: (err) => {
        if (err instanceof ApiError && err.code === 'reference_taken') setErrors({ reference: err.message });
      },
    });
  };

  if (create.data) {
    return {
      title: 'Your payment link is ready',
      body: <Created payment={create.data} businessName={merchant.businessName} />,
      footer: <Button onClick={close}>Done</Button>,
    };
  }
  // A reference already used is said on its own field, not above the form.
  const refusal = create.error instanceof ApiError && create.error.code === 'reference_taken' ? null : create.error;
  return {
    title: 'Request payment',
    body: (
      <form id={formId} onSubmit={submit} noValidate className="grid gap-4">
        <FormTop mode={mode} error={refusal} reusable={false} close={close} />
        {picker}
        <OnceFields draft={draft} errors={errors} set={set} />
      </form>
    ),
    footer: cancelAndSubmit(close, formId, create.isPending, 'Create link'),
  };
}

/** One link many customers pay, each entering the amount: a reusable link. */
function useOpenFlow({ mode, merchant, close, picker }: FlowProps): Flow {
  const formId = useId();
  const { draft, set } = useDraft<OpenLinkDraft>(emptyOpenLinkDraft);
  const [errors, setErrors] = useState<OpenLinkErrors>({});
  const create = useCreate((link: NewOpenLink, key) => createOpenLink(mode, link, key), ['open-links'], mode);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = checkOpenLink(draft);
    setErrors(found);
    if (Object.keys(found).length || create.isPending) return;
    create.mutate(toOpenLink(draft));
  };

  if (create.data) {
    return {
      title: 'Your reusable link is ready',
      body: <OpenCreated link={create.data} businessName={merchant.businessName} />,
      footer: (
        <>
          <Button asChild variant="secondary">
            <Link to={`/business/${mode}/links/open/${create.data.id}/poster`}><Printer className="size-4" aria-hidden />Print poster</Link>
          </Button>
          <Button onClick={close}>Done</Button>
        </>
      ),
    };
  }
  return {
    title: 'Request payment',
    body: (
      <form id={formId} onSubmit={submit} noValidate className="grid gap-4">
        <FormTop mode={mode} error={create.error} reusable close={close} />
        {picker}
        <OpenLinkFields draft={draft} errors={errors} set={set} />
      </form>
    ),
    footer: cancelAndSubmit(close, formId, create.isPending, 'Create link'),
  };
}

function OnceFields({ draft, errors, set }: {
  draft: OnceDraft;
  errors: OnceErrors;
  set: <K extends keyof OnceDraft>(field: K) => (value: OnceDraft[K]) => void;
}) {
  return (
    <>
      <Field label="Amount (₦)" error={errors.amount}>
        <Input inputMode="decimal" placeholder="5,000" value={draft.amount} onChange={(e) => set('amount')(e.target.value)} />
      </Field>
      <Field label="What it's for" hint="Your customer sees this when they pay." error={errors.description}>
        <Input maxLength={255} placeholder="Balance for LOS–ABV, 12 Oct" value={draft.description} onChange={(e) => set('description')(e.target.value)} />
      </Field>
      <Field label="Customer name (optional)" hint="Anyone with the link can see this name.">
        <Input maxLength={100} autoComplete="off" value={draft.customerName} onChange={(e) => set('customerName')(e.target.value)} />
      </Field>
      <Field label="Your reference (optional)" hint="Leave blank and we'll make one." error={errors.reference}>
        <Input maxLength={128} autoComplete="off" value={draft.reference} onChange={(e) => set('reference')(e.target.value)} />
      </Field>
      <div className="grid gap-1.5">
        <p aria-hidden className="text-[13px] leading-[18px] font-semibold text-text-secondary">Link open for</p>
        <Segmented legend="Link open for" name="days" value={draft.days} options={DAYS} onChange={set('days')} />
      </div>
    </>
  );
}

/**
 * The Idempotency middleware's refusal of a key reused with another body: an
 * uncoded 409 (a coded one is the handler's own). useIdempotencyKey already gives
 * each body its own key, so this is defence in depth — a key that somehow met a
 * different body still ends in a fresh key and a clear message, never a dead end.
 */
const isKeyConflict = (err: unknown) => err instanceof ApiError && err.status === 409 && !err.code;

/** Why the API refused, with the way forward when there is one. */
function Refusal({ error, mode, reusable, onLeave }: { error: Error; mode: Mode; reusable: boolean; onLeave: () => void }) {
  const code = error instanceof ApiError ? error.code : undefined;
  return (
    <FormError>
      <p>
        {isKeyConflict(error) ? 'This link may already have been made — check your payment links.' : error.message}
        {code === 'live_not_enabled' && <> <TestModeLink reusable={reusable} onClick={onLeave} /></>}
        {code === 'settlement_not_set' && <> <Link to={`/business/${mode}/payouts`} onClick={onLeave} className={inlineLink}>Set up payouts</Link></>}
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
      <LinkShare url={p.checkoutUrl} message={linkMessage(businessName, p)} card={linkCard(businessName, p)} />
    </div>
  );
}

function OpenCreated({ link: l, businessName }: { link: OpenLink; businessName: string }) {
  return (
    <div className="grid gap-4">
      <div>
        <p className="text-title3 font-semibold">{l.label}</p>
        <p className="text-body-sm text-text-secondary">{openAmountRule(l)} · each customer enters what they pay</p>
      </div>
      <LinkShare url={l.url} message={openLinkMessage(businessName, l)} card={openLinkCard(businessName, l)} />
    </div>
  );
}
