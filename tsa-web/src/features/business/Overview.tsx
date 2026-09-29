import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowRight, Check } from 'lucide-react';
import { useSession } from '@/session/context';
import { cn } from '@/lib/cn';
import { useMode } from '@/components/shell/mode';
import { AccentCard, Card, CardHeader } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/states';
import { listPayments, type Mode, type OwnMerchant } from './api';
import { BusinessPage } from './BusinessPage';
import { shortAddress } from './display';
import { paymentColumns } from './paymentColumns';
import { queryKeys } from './queryKeys';
import { NoPaymentsYet, PaymentsTable } from './PaymentsTable';

// `to` empty: nothing for the merchant to do but wait, so the step is not a link.
type Step = { done: boolean; title: string; todo: string; hint: string; to: string };

function steps(m: OwnMerchant, mode: Mode, testPaid: boolean): Step[] {
  const biz = (page: string) => `/business/${mode}/${page}`;
  const keys = [m.hasTestKey && 'Test', m.hasLiveKey && 'live'].filter(Boolean).join(' and ');
  const webhook = m.webhookUrl || m.testWebhookUrl;
  return [
    { done: true, title: 'Business approved', todo: '', hint: [m.businessName, m.websiteDomain].filter(Boolean).join(' · '), to: '' },
    {
      done: m.settlementMode !== '',
      title: 'Payouts set',
      todo: 'Set up payouts',
      hint: m.settlementMode === 'fiat' ? [m.accountNumber, m.accountName].filter(Boolean).join(' · ')
        : m.settlementMode === 'crypto' ? `${m.tokenSymbol} to ${shortAddress(m.walletAddress ?? '')}`
        : 'Tell us where to send your money',
      to: biz('payouts'),
    },
    {
      done: m.hasLiveKey || m.hasTestKey,
      title: 'API keys created',
      todo: 'Create your API keys',
      hint: keys ? keys.charAt(0).toUpperCase() + keys.slice(1) : 'Your server creates payments with them',
      to: biz('developers'),
    },
    {
      done: Boolean(webhook),
      title: 'Webhook added',
      todo: 'Add a webhook URL',
      hint: webhook ? webhook.replace(/^https?:\/\//, '') : 'So your site hears about payments',
      to: biz('developers'),
    },
    { done: testPaid, title: 'Test payment made', todo: 'Make a test payment', hint: 'In test mode — no real money moves', to: '/docs' },
    {
      done: m.liveEnabled,
      title: 'Live payments',
      todo: 'Live payments',
      hint: m.liveEnabled ? 'On — your live key takes real payments' : "Switched off — contact TSA Connect to turn them back on",
      to: '',
    },
  ];
}

function Checklist({ items }: { items: Step[] }) {
  const done = items.filter((s) => s.done).length;
  return (
    <section aria-label="Finish setting up">
      <AccentCard>
        <CardHeader title="Finish setting up" description={`${done} of ${items.length} done`} />
        <div className="px-5 pt-1 pb-2">
          <div className="mb-1 h-1.5 overflow-hidden rounded-full bg-surface-alt" aria-hidden>
            <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${(done / items.length) * 100}%` }} />
          </div>
          <ol className="grid xl:grid-cols-6 xl:gap-4">
            {items.map((s, i) => (
              <li key={s.title} className="flex items-start gap-3 border-b border-dashed border-hairline py-3 last:border-0 xl:border-0">
                <span
                  aria-hidden
                  className={cn(
                    'grid size-[22px] shrink-0 place-items-center rounded-full text-caption font-bold',
                    s.done ? 'bg-success-bg text-success-strong' : 'border-[1.5px] border-dashed border-border text-text-secondary',
                  )}
                >
                  {s.done ? <Check className="size-3.5" strokeWidth={3} /> : i + 1}
                </span>
                <div className="min-w-0">
                  {s.done ? (
                    <p className="text-body-sm font-semibold">
                      {s.title}
                      <span className="sr-only"> (done)</span>
                    </p>
                  ) : s.to ? (
                    <Link to={s.to} className="group text-body-sm font-semibold text-accent-strong hover:underline">
                      {s.todo}
                      <ArrowRight className="ml-1 inline size-3.5 align-[-2px] transition-transform group-hover:translate-x-0.5" aria-hidden />
                    </Link>
                  ) : (
                    <p className="text-body-sm font-semibold">{s.todo}</p>
                  )}
                  <p className="line-clamp-2 text-caption break-words text-text-secondary" title={s.hint}>{s.hint}</p>
                </div>
              </li>
            ))}
          </ol>
        </div>
      </AccentCard>
    </section>
  );
}

function greeting(now = new Date()) {
  const h = now.getHours();
  return h < 12 ? 'Good morning' : h < 17 ? 'Good afternoon' : 'Good evening';
}

function OverviewBody({ merchant }: { merchant: OwnMerchant }) {
  const mode = useMode();
  const { user } = useSession();
  const testPaid = useQuery({
    queryKey: queryKeys.list('payments', 'test', { status: 'succeeded', page: 1, limit: 1 }),
    queryFn: ({ signal }) => listPayments('test', { status: 'succeeded', limit: 1 }, signal),
  });
  const recent = useQuery({
    queryKey: queryKeys.list('payments', mode, { page: 1, limit: 5 }),
    queryFn: ({ signal }) => listPayments(mode, { limit: 5 }, signal),
  });
  // Wait for the test-payment answer so the card doesn't flash in and out.
  const list = testPaid.isPending ? null : steps(merchant, mode, (testPaid.data?.total ?? 0) > 0);
  const firstName = user?.name.trim().split(/\s+/)[0];

  return (
    // One minmax(0, 1fr) column: the payments table scrolls inside its card, not the page.
    <div className="grid grid-cols-1 gap-5">
      <div>
        <h2 className="text-title3 font-semibold">{greeting()}{firstName ? `, ${firstName}` : ''}</h2>
        <p className="text-body-sm text-text-secondary">
          {merchant.businessName} · {mode === 'test' ? 'test mode — no real money moves' : 'live payments'}
        </p>
      </div>

      {list && list.some((s) => !s.done) && <Checklist items={list} />}

      <Card>
        <CardHeader
          title="Recent payments"
          actions={
            <Link to={`/business/${mode}/payments`} className="inline-flex items-center gap-1 text-[13px] font-semibold text-accent-strong hover:underline">
              View all<ArrowRight className="size-3.5" aria-hidden />
            </Link>
          }
        />
        {recent.isError ? (
          <ErrorState title="We couldn't load your payments" body={recent.error.message} onRetry={() => void recent.refetch()} />
        ) : recent.data?.items.length === 0 ? (
          <NoPaymentsYet mode={mode} />
        ) : (
          <PaymentsTable items={recent.data?.items} loading={recent.isPending} columns={paymentColumns} hrefFor={(p) => `/business/${mode}/payments/${p.id}`} />
        )}
      </Card>
    </div>
  );
}

export function Overview() {
  return <BusinessPage title="Overview">{(m) => <OverviewBody merchant={m} />}</BusinessPage>;
}
