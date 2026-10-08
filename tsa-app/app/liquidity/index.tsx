// app/liquidity/index.tsx — TSA Liquidity dashboard (spec §17). Reads only
// GET /liquidity/me and /liquidity/me/activity; deposits go through
// /p2p/create-ad?mode=lp, withdrawals through /p2p/pool. Actuals only: no APR,
// no "interest" copy (spec §19). Amounts are the API's decimal strings (18 dp on
// the pool chain), formatted without floats.
import React, { useCallback, useRef, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Card, EmptyState, ScreenHeader, SegmentedControl, Spinner, SummaryRow, Text } from '@/components/ui';
import {
  liquidityApi,
  type LiquidityActivityItem,
  type LiquidityActivityPage,
  type LiquidityActivityType,
  type LiquidityMe,
} from '@/services/liquidityApi';
import { formatLiquidityAmount, liquidityStage } from '@/utils/liquidityView';
import { LP_APPLY_ROUTE } from '@/utils/lpCta';

const ADD_ROUTE = '/p2p/create-ad?mode=lp';
const isZero = (v?: string) => !v || /^0*(\.0*)?$/.test(v);

export default function LiquidityScreen() {
  const { colors, spacing } = useTheme();
  const [me, setMe] = useState<LiquidityMe | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [tab, setTab] = useState<LiquidityActivityType>('deposits');
  const [activity, setActivity] = useState<LiquidityActivityPage | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const tabRef = useRef(tab);
  tabRef.current = tab;
  // Only the latest activity read may land: a quick tab switch must not let a
  // slower response for the previous tab overwrite the list.
  const activityReq = useRef(0);

  const load = useCallback(async () => {
    const res = await liquidityApi.me();
    if (res.success && res.data) {
      setMe(res.data);
      setLoadFailed(false);
    } else {
      setLoadFailed(true);
    }
  }, []);
  const loadActivity = useCallback(async (t: LiquidityActivityType) => {
    const req = ++activityReq.current;
    const res = await liquidityApi.activity(t);
    if (req !== activityReq.current) return;
    setActivity(res.success && res.data ? res.data : { items: [] });
  }, []);
  useFocusEffect(
    useCallback(() => {
      load();
      loadActivity(tabRef.current);
    }, [load, loadActivity]),
  );

  const onRefresh = async () => {
    setRefreshing(true);
    await Promise.all([load(), loadActivity(tab)]);
    setRefreshing(false);
  };

  // The root stack hides headers; P2P is where this screen is entered from.
  const header = <ScreenHeader title="Your liquidity" fallbackRoute="/p2p" />;
  if (!me) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
        {header}
        {loadFailed ? (
          <EmptyState icon="warning" title="Couldn't load your liquidity" subtitle="Check your connection and try again."
            actionLabel="Try again" onAction={load} />
        ) : (
          <Spinner />
        )}
      </SafeAreaView>
    );
  }

  const stage = liquidityStage(me);
  const sym = me.token?.symbol ?? 'USDT';
  const amt = (v?: string) => `${formatLiquidityAmount(v)} ${sym}`;
  const withdrawRoute = `/p2p/pool?${me.chain ? `chainId=${me.chain.id}&` : ''}token=${encodeURIComponent(sym)}`;
  const activityValue = (it: LiquidityActivityItem) =>
    it.type === 'payout'
      ? [`₦${it.amountNgn ?? '0'}`, it.status, it.bankLast4 ? `••${it.bankLast4}` : undefined].filter(Boolean).join(' · ')
      : amt(it.amount);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
      {header}
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
      >
        {stage === 'none' && (
          <EmptyState icon="pool" title="Provide liquidity & earn"
            subtitle={`Add ${sym} to the TSA Connect pool (${me.limits.min}–${me.limits.max} ${sym}). When it sells, we pay you in naira.`}
            actionLabel="Apply" onAction={() => router.push(LP_APPLY_ROUTE as never)} />
        )}
        {stage === 'applied' && (
          <EmptyState icon="clock" title="Application under review" subtitle="We'll notify you when it's approved." />
        )}
        {stage === 'rejected' && (
          <EmptyState icon="close" title="Application not approved" subtitle="Check your notifications for the reason." />
        )}
        {stage === 'activate' && (
          <Card padding="md" style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">You&apos;re approved</Text>
            <Text variant="caption" color="textSecondary">Activate to start adding {sym}.</Text>
            <Button title="Activate" variant="primary" onPress={() => router.push('/merchants/activate-pm' as never)} />
          </Card>
        )}
        {(stage === 'active' || stage === 'paused') && (
          <>
            {stage === 'paused' && (
              <Text variant="bodySm" color="warning">
                Your liquidity is paused. It is not offered to buyers; you can still withdraw.
              </Text>
            )}
            {!me.poolOpen && (
              <Text variant="bodySm" color="textSecondary">
                The pool is closed right now. Your balance is safe and you can withdraw.
              </Text>
            )}
            {(me.voucher?.renewDue || me.voucher?.status === 'paused') && (
              <Card padding="md" style={{ gap: spacing.sm }}>
                <Text variant="bodyMed" weight="700">Renew to stay in the pool</Text>
                <Button title="Renew" variant="primary" disabled={!me.poolOpen || stage === 'paused'}
                  onPress={() => router.push(ADD_ROUTE as never)} />
              </Card>
            )}
            <Card padding="md">
              <Text variant="caption" color="textSecondary">{me.chain?.name ?? 'Pool network'} · {sym}</Text>
              {me.balanceReadable ? (
                <>
                  <SummaryRow label="Available" value={amt(me.available)} />
                  <SummaryRow label="Locked in orders" value={amt(me.locked)} />
                  <SummaryRow label="In the pool" value={amt(me.stake)} total />
                  {!isZero(me.aboveMax) && <SummaryRow label="Above your limit (not in the pool)" value={amt(me.aboveMax)} />}
                  <SummaryRow label="Limits" value={`${me.limits.min}–${me.limits.max} ${sym}`} />
                </>
              ) : (
                <Text variant="bodySm" color="textSecondary">We can&apos;t read your balance right now. Pull to refresh.</Text>
              )}
              <SummaryRow label="Owed to you" value={`₦${me.owedNgn}`} />
            </Card>
            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <View style={{ flex: 1 }}>
                <Button title="Add liquidity" variant="primary" disabled={stage === 'paused' || !me.poolOpen}
                  onPress={() => router.push(ADD_ROUTE as never)} />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Withdraw" variant="secondary" onPress={() => router.push(withdrawRoute as never)} />
              </View>
            </View>
            <SegmentedControl<LiquidityActivityType>
              segments={[
                { key: 'deposits', label: 'Deposits' },
                { key: 'withdrawals', label: 'Withdrawals' },
                { key: 'payouts', label: 'Payouts' },
              ]}
              value={tab}
              onChange={(t) => {
                setTab(t);
                setActivity(null);
                loadActivity(t);
              }}
            />
            {activity == null ? (
              <Spinner />
            ) : activity.items.length ? (
              <Card padding="md">
                {activity.items.map((it) => (
                  <SummaryRow key={it.id} label={new Date(it.createdAt).toLocaleString()} value={activityValue(it)} />
                ))}
              </Card>
            ) : (
              <Text variant="bodySm" color="textSecondary">Nothing yet.</Text>
            )}
          </>
        )}
      </ScrollView>
    </SafeAreaView>
  );
}
