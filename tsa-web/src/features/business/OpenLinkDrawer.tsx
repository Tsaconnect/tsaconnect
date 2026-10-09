import { useId, useState, type FormEvent } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Pause, Play, Printer, TriangleAlert } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { naira } from '@/lib/format';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Drawer } from '@/components/ui/drawer';
import { FormError } from '@/components/ui/form-error';
import { inlineLink } from '@/components/ui/inline-link';
import { Note } from '@/components/ui/note';
import { SectionHeading } from '@/components/ui/section-heading';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import { toast } from '@/components/ui/toaster';
import {
  openLinkChange, replaceOpenLink, updateOpenLink,
  type Mode, type OpenLink, type OpenLinkDetail, type Payment,
} from './api';
import { openAmountRule, openLinkCard, openLinkMessage, openLinkTaking } from './display';
import { LinkShare } from './LinkShare';
import { TestModeLink } from './LiveOffNote';
import { OpenLinkState } from './OpenLinkState';
import { openLinkPaymentColumns } from './paymentColumns';
import { PaymentsTable } from './PaymentsTable';
import { OpenLinkFields } from './OpenLinkFields';
import { checkOpenLink, openLinkDraft, toOpenLink, type OpenLinkDraft, type OpenLinkErrors } from './openLinkForm';
import { useMerchant } from './useMerchant';
import { queryKeys } from './queryKeys';
import { useOpenLink } from './useOpenLink';

/** The links list's query showing reusable links, its other filters kept (arriving from the poster, there are none). */
function reusableList(search: string) {
  const params = new URLSearchParams(search);
  params.set('type', 'reusable');
  return `?${params}`;
}

/** A reusable link over the links list: share it, see what it has taken, pause, replace or print it. */
export function OpenLinkDrawer() {
  const mode = useMode();
  const id = useParams().id ?? '';
  const navigate = useNavigate();
  const { search } = useLocation();
  const q = useOpenLink(mode, id);
  const notFound = q.error instanceof ApiError && q.error.status === 404;

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) navigate({ pathname: `/business/${mode}/links`, search: reusableList(search) }, { replace: true });
      }}
      title={q.data?.openLink.label ?? 'Reusable link'}
    >
      {q.data ? (
        <Detail detail={q.data} mode={mode} />
      ) : q.isPending ? (
        <div className="grid gap-3" aria-hidden>
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="size-[148px]" />
        </div>
      ) : notFound ? (
        <ErrorState title="Link not found" body={`There's no ${mode} reusable link with this ID.`} />
      ) : (
        <ErrorState title="We couldn't load this link" body={q.error?.message} onRetry={() => void q.refetch()} />
      )}
    </Drawer>
  );
}

function Detail({ detail: { openLink: l, payments }, mode }: { detail: OpenLinkDetail; mode: Mode }) {
  const merchant = useMerchant().data;
  const locked = merchant?.status === 'suspended';
  const queryClient = useQueryClient();
  const [replacing, setReplacing] = useState(false);
  const [editing, setEditing] = useState(false);
  const taking = openLinkTaking(l);

  // What changed is shown at once; the list's counts and states are read again.
  const saved = (next: OpenLink) => {
    queryClient.setQueryData<OpenLinkDetail>(queryKeys.openLink(mode, next.id), (old) => ({ openLink: next, payments: old?.payments ?? payments }));
    void queryClient.invalidateQueries({ queryKey: queryKeys.list('open-links', mode) });
  };
  const pause = useMutation({
    mutationFn: (active: boolean) => updateOpenLink(mode, l.id, { ...openLinkChange(l), active }),
    onSuccess: (next) => {
      saved(next);
      toast.success(next.active ? 'Link is taking payments again' : 'Link paused');
    },
    onError: (err) => {
      toast.error(err.message);
      // Changed somewhere else first: read it again before the next try.
      if (err instanceof ApiError && err.status === 409) void queryClient.invalidateQueries({ queryKey: queryKeys.openLink(mode, l.id) });
    },
  });

  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <OpenLinkState link={l} />
        <span className="text-body-sm text-text-secondary">{openAmountRule(l)}</span>
      </div>
      <p className="mt-2.5 text-body-sm text-text-secondary">
        You received <b className="text-title1 font-bold text-text-primary tabular">{naira(l.receivedTotal)}</b>{' '}
        from {l.paidCount} {l.paidCount === 1 ? 'payment' : 'payments'}
      </p>

      {!taking.taking && (
        <Note tone="warning" icon={<Pause />} className="mt-4">
          {taking.reason}. Customers who open this link are told it isn't taking payments.
          {l.notTakingReason === 'no_payout' && <> <Link to={`/business/${mode}/payouts`} className={inlineLink}>Set up payouts</Link></>}
          {l.notTakingReason === 'live_off' && <> <TestModeLink reusable /></>}
          {l.notTakingReason === 'range_outside_limits' && !locked && (
            <> <button type="button" onClick={() => setEditing(true)} className={inlineLink}>Edit the link</button></>
          )}
        </Note>
      )}

      <SectionHeading>Share</SectionHeading>
      <LinkShare url={l.url} message={openLinkMessage(merchant?.businessName ?? '', l)} card={openLinkCard(merchant?.businessName ?? '', l)} qrSize={148} />

      <div className="mt-4 flex flex-wrap gap-2">
        <Button asChild size="sm" variant="secondary">
          <Link to={`/business/${mode}/links/open/${l.id}/poster`}><Printer className="size-4" aria-hidden />Print poster</Link>
        </Button>
        <Button size="sm" variant="secondary" loading={pause.isPending} disabled={locked} onClick={() => pause.mutate(!l.active)}>
          {l.active ? <><Pause className="size-4" aria-hidden />Pause</> : <><Play className="size-4" aria-hidden />Resume</>}
        </Button>
        <Button size="sm" variant="secondary" disabled={locked} onClick={() => setEditing(true)}>Edit</Button>
        <Button size="sm" variant="danger" disabled={locked} onClick={() => setReplacing(true)}>Replace link</Button>
      </div>

      <SectionHeading>Recent payments</SectionHeading>
      <RecentPayments items={payments} mode={mode} />

      {editing && <EditDialog link={l} mode={mode} onClose={() => setEditing(false)} onSaved={saved} />}
      {replacing && <ReplaceDialog link={l} mode={mode} onClose={() => setReplacing(false)} onReplaced={saved} />}
    </>
  );
}

function RecentPayments({ items, mode }: { items: Payment[]; mode: Mode }) {
  if (items.length === 0) return <p className="text-body-sm text-text-secondary">No one has paid through this link yet.</p>;
  return (
    <div className="rounded-md border border-hairline">
      <PaymentsTable items={items} loading={false} columns={openLinkPaymentColumns} hrefFor={(p) => `/business/${mode}/payments/${p.id}`} />
    </div>
  );
}

/**
 * Label, limits and payment expiry, checked as when the link was made. Saved against the version
 * on screen; if the link changed elsewhere first it is read again (what was typed
 * stays) and the next Save goes against the fresh one.
 */
function EditDialog({ link: l, mode, onClose, onSaved }: { link: OpenLink; mode: Mode; onClose: () => void; onSaved: (next: OpenLink) => void }) {
  const queryClient = useQueryClient();
  const formId = useId();
  const [draft, setDraft] = useState<OpenLinkDraft>(() => openLinkDraft(l));
  const [errors, setErrors] = useState<OpenLinkErrors>({});
  const set = (field: keyof OpenLinkDraft) => (value: string) => setDraft((d) => ({ ...d, [field]: value }));
  const save = useMutation({
    mutationFn: () => updateOpenLink(mode, l.id, { ...toOpenLink(draft), active: l.active, expectedVersion: l.version }),
    onSuccess: (next) => {
      onSaved(next);
      onClose();
      toast.success('Link saved');
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) void queryClient.invalidateQueries({ queryKey: queryKeys.openLink(mode, l.id) });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = checkOpenLink(draft);
    setErrors(found);
    if (Object.keys(found).length === 0 && !save.isPending) save.mutate();
  };
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Edit link"
      focusFirstField
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form={formId} loading={save.isPending}>Save</Button>
        </>
      }
    >
      <form id={formId} onSubmit={submit} noValidate className="grid gap-4">
        {save.error && <FormError>{save.error.message}</FormError>}
        <OpenLinkFields draft={draft} errors={errors} set={set} />
      </form>
    </Dialog>
  );
}

/** A new address, asked first: every poster already printed stops working. */
function ReplaceDialog({ link: l, mode, onClose, onReplaced }: { link: OpenLink; mode: Mode; onClose: () => void; onReplaced: (next: OpenLink) => void }) {
  const replace = useMutation({
    mutationFn: () => replaceOpenLink(mode, l.id),
    onSuccess: (next) => {
      onReplaced(next);
      onClose();
      toast.success('New link ready — print the new poster');
    },
  });
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Replace this link?"
      description="Your printed posters will stop working. Print the new one."
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Keep this link</Button>
          <Button variant="danger" loading={replace.isPending} onClick={() => replace.mutate()}>Replace link</Button>
        </>
      }
    >
      {replace.error ? (
        <FormError>{replace.error.message}</FormError>
      ) : (
        <Note tone="warning" icon={<TriangleAlert />}>Customers who open the old link or scan an old poster are told it isn't taking payments.</Note>
      )}
    </Dialog>
  );
}
