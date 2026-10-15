import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Coins, Smartphone, TriangleAlert } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { dateTime } from '@/lib/format';
import { AccentCard, Card, CardBody, CardHeader } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Note } from '@/components/ui/note';
import { Skeleton } from '@/components/ui/skeleton';
import { EmptyState, ErrorState } from '@/components/ui/states';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { toast } from '@/components/ui/toaster';
import { BusinessPage } from './BusinessPage';
import { useIdempotencyKey } from './useIdempotencyKey';
import {
  activitySign, canClaim, claimErrorMessage, claimLiquidity, oldWalletHasFunds, oldWalletOnlyPending, getLiquidity, isZero,
  LIQUIDITY_ACTIVITY_KEY, LIQUIDITY_KEY, listLiquidityActivity, tokenAmount,
  type LiquidityActivity, type LiquidityMe,
} from './liquidityApi';

const KIND_LABEL: Record<LiquidityActivity['kind'], string> = {
  deposit: 'Added',
  withdrawal: 'Withdrawn',
  claim: 'Claimed',
  earned: 'Earned',
  withdrawal_pending: 'Withdrawal pending',
  withdrawal_paid: 'Withdrawal paid',
};

function Figure({ label, value, strong }: { label: string; value: string; strong?: boolean }) {
  return (
    <div>
      <p className="text-caption text-text-secondary">{label}</p>
      <p className={strong ? 'text-[22px] leading-8 font-semibold' : 'text-body font-semibold'}>{value}</p>
    </div>
  );
}

function Activity() {
  const q = useInfiniteQuery({
    queryKey: LIQUIDITY_ACTIVITY_KEY,
    queryFn: ({ pageParam, signal }) => listLiquidityActivity(pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Card>
      <CardHeader title="Earnings history" description="Your share of every sale, and what you added, withdrew and claimed." />
      {q.isPending ? (
        <CardBody><Skeleton className="h-5 w-full" /></CardBody>
      ) : q.isError ? (
        <ErrorState title="We couldn't load your history" body={q.error.message} onRetry={() => void q.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title="Nothing yet" body="Earnings appear here as buyers get USDT from the pool." />
      ) : (
        <>
          <Table>
            <THead>
              <TR><TH>When</TH><TH>What</TH><TH className="text-right">Amount</TH></TR>
            </THead>
            <TBody>
              {items.map((it) => (
                <TR key={`${it.kind}:${it.txHash}:${it.at}`}>
                  <TD>{dateTime(it.at)}</TD>
                  <TD>{KIND_LABEL[it.kind]}</TD>
                  <TD className="text-right tabular-nums">
                    {activitySign(it.kind)}{tokenAmount(it.amount)} USDT
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          {q.hasNextPage && (
            <CardBody>
              <Button variant="secondary" size="sm" loading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>Load more</Button>
            </CardBody>
          )}
        </>
      )}
    </Card>
  );
}

function Position({ me }: { me: LiquidityMe }) {
  const queryClient = useQueryClient();
  const idem = useIdempotencyKey();
  const sym = me.token?.symbol ?? 'USDT';
  const amt = (v?: string) => `${tokenAmount(v)} ${sym}`;
  const claim = useMutation({
    mutationFn: () => claimLiquidity(idem.keyFor({ claim: me.claimable })),
    onSuccess: () => {
      toast.success(`Your earnings are on the way to your wallet ${me.wallet ?? ''}.`);
      void queryClient.invalidateQueries({ queryKey: LIQUIDITY_KEY });
      void queryClient.invalidateQueries({ queryKey: LIQUIDITY_ACTIVITY_KEY });
    },
    onError: (err) => {
      idem.forget();
      toast.error(claimErrorMessage(err));
    },
  });
  const claimable = canClaim(me);

  return (
    <>
      {me.paused && (
        <Note tone="warning" icon={<TriangleAlert />}>Your liquidity is paused: you cannot add more. You can still withdraw and claim.</Note>
      )}
      {me.walletChanged && (
        <Note tone="warning" icon={<TriangleAlert />}>
          Your account now uses a different wallet. Earnings are paid to {me.wallet}.{' '}
          {oldWalletOnlyPending(me)
            ? <>A withdrawal from your old wallet is still pending. You can switch wallets once it completes.</>
            : oldWalletHasFunds(me)
            ? <>Withdraw and claim from {me.wallet} first (switch to it in the TSA Connect app), then activate your new wallet there.</>
            : <>Activate your new wallet in the TSA Connect app to add liquidity from it.</>}
        </Note>
      )}
      <AccentCard>
        <CardHeader
          title="Spread earnings"
          description={`Earnings go to your TSA Connect wallet ${me.wallet ?? ''}. Cash them out to your bank or mobile money from the app.`}
          actions={
            <Button onClick={() => claim.mutate()} loading={claim.isPending} disabled={!claimable}>
              Claim {amt(me.claimable)}
            </Button>
          }
        />
        <CardBody className="grid grid-cols-2 gap-5 md:grid-cols-4">
          {me.balanceReadable ? (
            <>
              <Figure label="Ready to claim" value={amt(me.claimable)} strong />
              <Figure label="Earned all time" value={amt(me.earned)} />
              <Figure label="Your liquidity" value={amt(me.principal)} />
              <Figure label="Share of the pool" value={`${me.sharePercent ?? '0'}%`} />
            </>
          ) : (
            <p className="col-span-full text-body-sm text-text-secondary">We can&apos;t read your balance right now. Try again in a minute.</p>
          )}
        </CardBody>
        {me.balanceReadable && me.lockedUntil && (
          <CardBody className="pt-0 text-caption text-text-secondary">
            {amt(me.withdrawable)} can be withdrawn now. New deposits unlock 24 hours after they are added; the rest unlocks {dateTime(me.lockedUntil)}. Earnings can always be claimed.
          </CardBody>
        )}
        {me.balanceReadable && !isZero(me.pendingWithdrawal) && (
          <CardBody className="pt-0 text-caption text-text-secondary">Pending withdrawal: {amt(me.pendingWithdrawal)}</CardBody>
        )}
        {!claimable && !me.walletChanged && (
          <CardBody className="pt-0 text-caption text-text-secondary">Claims start from {me.minClaim} {sym} here; smaller amounts can be claimed from the app.</CardBody>
        )}
      </AccentCard>
    </>
  );
}

function PoolCard({ me }: { me: LiquidityMe }) {
  if (!me.pool) return null;
  const sym = me.token?.symbol ?? 'USDT';
  return (
    <Card>
      <CardHeader title="TSA Connect pool" description={me.chain?.name} />
      <CardBody className="grid grid-cols-2 gap-5 md:grid-cols-3">
        {me.pool.totalLiquidity != null && <Figure label="Total liquidity" value={`${tokenAmount(me.pool.totalLiquidity)} ${sym}`} />}
        {me.pool.apr30d != null && <Figure label="Earnings rate, last 30 days" value={`${me.pool.apr30d}% a year`} />}
      </CardBody>
      {me.pool.apr30d != null && (
        <CardBody className="pt-0 text-caption text-text-secondary">Past earnings are not a promise.</CardBody>
      )}
    </Card>
  );
}

function NotProviding({ me }: { me: LiquidityMe }) {
  const sym = me.token?.symbol ?? 'USDT';
  return (
    <Card>
      <EmptyState
        icon={<Coins />}
        title="Provide liquidity and earn"
        body={
          <>
            Add {me.limits.min}–{me.limits.max} {sym} to the TSA Connect pool and earn a share of the spread on every sale.
            Activate and add liquidity in the TSA Connect app (Profile → Provide liquidity &amp; earn);
            your earnings then show here.
          </>
        }
        action={<span className="inline-flex items-center gap-1.5 text-caption text-text-secondary"><Smartphone className="size-4" aria-hidden /> In the app</span>}
      />
    </Card>
  );
}

export function Liquidity() {
  const q = useQuery({ queryKey: LIQUIDITY_KEY, queryFn: ({ signal }) => getLiquidity(signal), retry: false });
  return (
    <BusinessPage title="Liquidity earnings">
      {() => {
        if (q.isPending) return <Card className="p-5"><Skeleton className="h-16 w-full" /></Card>;
        if (q.isError) {
          if (q.error instanceof ApiError && q.error.status === 404) {
            return <Card><EmptyState title="Not available yet" body="TSA Liquidity isn't open yet. We'll let you know when it is." /></Card>;
          }
          return <Card><ErrorState title="We couldn't load your liquidity" body={q.error.message} onRetry={() => void q.refetch()} /></Card>;
        }
        const me = q.data;
        const isLp = me.status === 'active' || me.status === 'paused';
        return (
          <div className="grid gap-4">
            {isLp ? <Position me={me} /> : <NotProviding me={me} />}
            <PoolCard me={me} />
            {isLp && <Activity />}
          </div>
        );
      }}
    </BusinessPage>
  );
}
