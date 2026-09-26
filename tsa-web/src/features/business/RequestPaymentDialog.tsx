import { useId, useRef, useState, type FormEvent, type ReactNode } from 'react';
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
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { Segmented } from '@/components/ui/segmented';
import {
  createLink, createOpenLink,
  type LinkDays, type Mode, type NewLink, type NewOpenLink, type OpenLink, type OwnMerchant, type Payment,
} from './api';
import { isLiveOff, linkMessage, openAmountRule, openLinkMessage } from './display';
import { LinkShare } from './LinkShare';
import { LiveOffNote } from './LiveOffNote';
import { OpenLinkFields } from './OpenLinkFields';
import { AMOUNT_HINT, checkOpenLink, emptyOpenLinkDraft, toOpenLink, type OpenLinkDraft, type OpenLinkErrors } from './openLinkForm';

const DAYS = [
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

type Draft = OpenLinkDraft & {
  kind: LinkKind;
  amount: string; description: string; customerName: string; reference: string; days: (typeof DAYS)[number]['value'];
};
type Errors = OpenLinkErrors & Partial<Record<'amount' | 'description' | 'reference', string>>;

function check(d: Draft): Errors {
  if (d.kind === 'open') return checkOpenLink(d);
  const errors: Errors = {};
  if (parseNaira(d.amount) === null) errors.amount = AMOUNT_HINT;
  if (!d.description.trim()) errors.description = 'Say what the payment is for — your customer sees this.';
  return errors;
}

type Request = { kind: 'once'; link: NewLink } | { kind: 'open'; link: NewOpenLink };
type Made = { kind: 'once'; payment: Payment } | { kind: 'open'; openLink: OpenLink };

/**
 * "Request payment": a form for a payment link, then the link to share. Mounted
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
  return open ? <Flow key={mode} onOpenChange={onOpenChange} merchant={merchant} kind={kind} /> : null;
}

function Flow({ onOpenChange, merchant, kind }: { onOpenChange: (open: boolean) => void; merchant: OwnMerchant; kind: LinkKind }) {
  const mode = useMode();
  const queryClient = useQueryClient();
  const formId = useId();
  // One Idempotency-Key per request body: sending the same form again (a reply
  // lost on the way) replays the link already made; any edit is a new request, so
  // it never meets the API's refusal of a key reused with a different body.
  const sent = useRef<{ body: string; key: string } | null>(null);
  const keyFor = (req: Request) => {
    const body = JSON.stringify(req);
    if (sent.current?.body !== body) sent.current = { body, key: crypto.randomUUID() };
    return sent.current.key;
  };
  const [draft, setDraft] = useState<Draft>({
    ...emptyOpenLinkDraft, kind, amount: '', description: '', customerName: '', reference: '', days: '7',
  });
  const [errors, setErrors] = useState<Errors>({});
  const close = () => onOpenChange(false);

  const listKey = (k: LinkKind) => [k === 'open' ? 'open-links' : 'links', mode];
  const create = useMutation({
    mutationFn: async (req: Request): Promise<Made> =>
      req.kind === 'open'
        ? { kind: 'open', openLink: await createOpenLink(mode, req.link, keyFor(req)) }
        : { kind: 'once', payment: await createLink(mode, req.link, keyFor(req)) },
    onSuccess: (made) => {
      void queryClient.invalidateQueries({ queryKey: listKey(made.kind) });
      if (made.kind === 'once') void queryClient.invalidateQueries({ queryKey: ['payments', mode] });
    },
    onError: (err, req) => {
      if (err instanceof ApiError && err.code === 'reference_taken') setErrors({ reference: err.message });
      // The key was already used for something else: start a new one, and let the
      // list behind show whether the first try made a link after all.
      if (isKeyConflict(err)) {
        sent.current = null;
        void queryClient.invalidateQueries({ queryKey: listKey(req.kind) });
      }
    },
  });

  const set = (field: keyof Draft) => (value: string) => setDraft((d) => ({ ...d, [field]: value }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = check(draft);
    setErrors(found);
    if (Object.keys(found).length || create.isPending) return;
    if (draft.kind === 'open') {
      create.mutate({ kind: 'open', link: toOpenLink(draft) });
      return;
    }
    create.mutate({
      kind: 'once',
      link: {
        amount: parseNaira(draft.amount)!,
        description: draft.description.trim(),
        customerName: draft.customerName.trim(),
        reference: draft.reference.trim(),
        expiresInDays: Number(draft.days) as LinkDays,
      },
    });
  };

  const made = create.data;
  // Live is switched off for this business: say so before the form, not after it.
  const liveOff = isLiveOff(merchant, mode);
  const refusal = create.error && !(create.error instanceof ApiError && create.error.code === 'reference_taken') ? create.error : null;
  const footer = made?.kind === 'open' ? (
    <>
      <Button asChild variant="secondary">
        <Link to={`/business/${mode}/links/open/${made.openLink.id}/poster`}><Printer className="size-4" aria-hidden />Print poster</Link>
      </Button>
      <Button onClick={close}>Done</Button>
    </>
  ) : made ? (
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
    <Dialog
      open
      onOpenChange={onOpenChange}
      title={made?.kind === 'open' ? 'Your reusable link is ready' : made ? 'Your payment link is ready' : 'Request payment'}
      footer={footer}
    >
      {made?.kind === 'open' ? (
        <OpenCreated link={made.openLink} businessName={merchant.businessName} />
      ) : made ? (
        <Created payment={made.payment} businessName={merchant.businessName} />
      ) : liveOff ? (
        <LiveOffNote page="links" doInstead="make a test link" onLeave={close} />
      ) : (
        <form id={formId} onSubmit={submit} noValidate className="grid gap-4">
          {mode === 'test' && <Note tone="gold" icon={<FlaskConical />}>This is a test link — no real money moves.</Note>}
          {refusal && <Refusal error={refusal} mode={mode} onLeave={close} />}
          <Segmented legend="Type of link" name="kind" value={draft.kind} options={KINDS} onChange={set('kind')} />
          {draft.kind === 'open' ? (
            <OpenLinkFields draft={draft} errors={errors} set={set} />
          ) : (
            <OnceFields draft={draft} errors={errors} set={set} />
          )}
        </form>
      )}
    </Dialog>
  );
}

function OnceFields({ draft, errors, set }: { draft: Draft; errors: Errors; set: (field: keyof Draft) => (value: string) => void }) {
  return (
    <>
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
    </>
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
      <LinkShare url={p.checkoutUrl} message={linkMessage(businessName, p)} />
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
      <LinkShare url={l.url} message={openLinkMessage(businessName, l)} />
    </div>
  );
}
