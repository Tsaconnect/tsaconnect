// app/p2p/my-trades.tsx
//
// Trades the current user is a party to. A SegmentedControl switches between
// Buyer and Seller roles, then a Chip picker narrows to Active (in-flight) or
// Done. Restyled onto the TSA Signature design system — behavior unchanged.
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { formatTokenAmount, tradesApi, type P2PTrade } from '../../services/p2p';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Chip,
  Divider,
  EmptyState,
  Icon,
  PressableScale,
  SegmentedControl,
  SkeletonGroup,
  StatusPill,
  Text,
} from '@/components/ui';

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

type Role = 'buyer' | 'seller';
type Bucket = 'active' | 'done';

const ACTIVE_STATUSES: P2PTrade['status'][] = [
  'pending_payment',
  'paid',
  'releasing',
  'disputed',
];

const MyTradesScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const [role, setRole] = useState<Role>('buyer');
  const [bucket, setBucket] = useState<Bucket>('active');
  const [trades, setTrades] = useState<P2PTrade[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await tradesApi.list({ role });
    if (res.success && res.data) setTrades(res.data);
    setLoading(false);
    setRefreshing(false);
  }, [role]);

  // useFocusEffect only fires on screen focus events, so a role-tab swap
  // while already focused wouldn't re-fetch. Pair it with a plain useEffect
  // on `load` so any dependency change (role) refreshes the list.
  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));
  useEffect(() => {
    setLoading(true);
    load();
  }, [load]);

  const visible = useMemo(() => {
    const active = new Set<P2PTrade['status']>(ACTIVE_STATUSES);
    return trades.filter((t) => (bucket === 'active' ? active.has(t.status) : !active.has(t.status)));
  }, [trades, bucket]);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            tintColor={colors.accent}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text variant="title1" weight="700">
          My Trades
        </Text>

        <View style={{ marginTop: spacing.md }}>
          <SegmentedControl
            segments={[
              { key: 'buyer', label: 'As buyer' },
              { key: 'seller', label: 'As seller' },
            ]}
            value={role}
            onChange={(k) => setRole(k as Role)}
          />
        </View>

        <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md, marginBottom: spacing.lg }}>
          {([
            { key: 'active', label: 'Active' },
            { key: 'done', label: 'Done' },
          ] as { key: Bucket; label: string }[]).map((b) => (
            <Chip key={b.key} label={b.label} selected={bucket === b.key} onPress={() => setBucket(b.key)} />
          ))}
        </View>

        {loading ? (
          <SkeletonGroup shape="tradeRow" count={4} />
        ) : visible.length === 0 ? (
          <EmptyState
            icon="swapVertical"
            title={bucket === 'active' ? 'No active trades' : 'No completed trades'}
            subtitle={
              bucket === 'active'
                ? `You have no in-flight trades as ${role}.`
                : `No completed trades as ${role} yet.`
            }
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {visible.map((t) => (
              <TradeCard key={t.id} trade={t} role={role} />
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const TradeCard: React.FC<{ trade: P2PTrade; role: Role }> = ({ trade, role }) => {
  const { colors, spacing } = useTheme();
  const decimals = TOKEN_DECIMALS[trade.token] ?? 18;
  const amount = formatTokenAmount(trade.tokenAmount, decimals, 4);
  const counterparty = role === 'buyer' ? trade.sellerId : trade.buyerId;

  return (
    <PressableScale onPress={() => router.push(`/p2p/trade/${trade.id}`)}>
      <AccentCard>
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
            <AmountText value={amount} variant="title2" />
            <Text variant="bodySm" color="textMuted">
              {trade.token}
            </Text>
          </View>
          <StatusPill kind="trade" status={trade.status} size="sm" />
        </View>

        <Divider spacing="md" />

        <MetaRow label="Fiat">
          <AmountText value={trade.fiatAmount} currency={trade.fiatCurrency} variant="bodySm" />
        </MetaRow>
        <MetaRow label="Rate">
          <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
            <AmountText value={trade.rate} currency={trade.fiatCurrency} variant="bodySm" />
            <Text variant="caption" color="textMuted">
              / {trade.token}
            </Text>
          </View>
        </MetaRow>
        <MetaRow label={role === 'buyer' ? 'Seller' : 'Buyer'}>
          <Text variant="bodySm" weight="600" numeric>
            {counterparty.slice(0, 8)}
          </Text>
        </MetaRow>
        <MetaRow label="Updated">
          <Text variant="bodySm" weight="600" color="textSecondary">
            {formatRelative(trade.updatedAt)}
          </Text>
        </MetaRow>

        {trade.status === 'released' && (
          <PressableScale
            onPress={() => router.push(`/p2p/trade/${trade.id}`)}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              gap: spacing.xs,
              marginTop: spacing.md,
              paddingTop: spacing.md,
              borderTopWidth: 1,
              borderTopColor: colors.hairline,
            }}
          >
            <Icon name="star" size={14} color={colors.accent} />
            <Text variant="caption" weight="700" color="accent">
              Rate this trade
            </Text>
          </PressableScale>
        )}
      </AccentCard>
    </PressableScale>
  );
};

const MetaRow: React.FC<{ label: string; children: React.ReactNode }> = ({ label, children }) => {
  const { spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingVertical: spacing.xs,
      }}
    >
      <Text variant="bodySm" color="textSecondary">
        {label}
      </Text>
      {children}
    </View>
  );
};

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return iso;
  if (ms < 60_000) return 'just now';
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h ago`;
  return `${Math.floor(ms / 86_400_000)}d ago`;
}

export default MyTradesScreen;
