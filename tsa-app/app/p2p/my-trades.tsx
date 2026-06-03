// app/p2p/my-trades.tsx
//
// Trades the current user is a party to. A SegmentedControl switches between
// Buyer and Seller roles, then a Chip picker narrows to Active (in-flight) or
// Done. Restyled onto the TSA Signature design system — behavior unchanged.
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { formatTokenAmount, tradesApi, type P2PTrade } from '../../services/p2p';
import { useAuth } from '../../AuthContext/AuthContext';
import { NotificationBell } from '../../components/p2p/NotificationBell';
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

type Role = 'all' | 'buyer' | 'seller';
type Bucket = 'active' | 'done';

const ACTIVE_STATUSES: P2PTrade['status'][] = [
  'pending_payment',
  'paid',
  'releasing',
  'disputed',
];

const MyTradesScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { currentUser } = useAuth();
  const userId = (currentUser as { id?: string } | null)?.id;
  // Default to "All" so the screen shows every trade the user is a party to —
  // a role-scoped default (e.g. "As buyer") looks empty for a seller-only user
  // and reads as "no history".
  const [role, setRole] = useState<Role>('all');
  const [bucket, setBucket] = useState<Bucket>('active');
  const [trades, setTrades] = useState<P2PTrade[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  // Tracks the role we've already auto-evaluated the bucket for (see effect
  // below) — keyed by role, not a one-shot, so switching roles re-evaluates.
  const autoBucketRoleRef = useRef<Role | null>(null);
  // Mirrors the currently-selected role so a slow fetch from a previous role
  // can't overwrite the list after the user switched (stale-response race).
  const roleRef = useRef(role);
  roleRef.current = role;

  const load = useCallback(async () => {
    const reqRole = role;
    // Omitting role returns all trades where the user is buyer OR seller.
    const res = await tradesApi.list(reqRole === 'all' ? {} : { role: reqRole });
    // Drop a stale response whose role no longer matches the selection.
    if (roleRef.current !== reqRole) return;
    if (res.success && res.data) setTrades(res.data);
    setLoading(false);
    setRefreshing(false);
  }, [role]);

  // useFocusEffect re-runs on focus AND whenever `load` changes while focused
  // (i.e. on a role switch, since `load` is keyed on role), so it already covers
  // mount + focus + role changes. A second [load] effect would only double-fetch.
  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));

  const { visible, activeCount, doneCount } = useMemo(() => {
    const active = new Set<P2PTrade['status']>(ACTIVE_STATUSES);
    const actives = trades.filter((t) => active.has(t.status));
    const dones = trades.filter((t) => !active.has(t.status));
    return {
      visible: bucket === 'active' ? actives : dones,
      activeCount: actives.length,
      doneCount: dones.length,
    };
  }, [trades, bucket]);

  // Once per role, after its data loads: if the default 'active' bucket is empty
  // but completed trades exist, land on 'Done' so the user doesn't see an empty
  // list and conclude there's no history. Keyed by role (not a one-shot) so a
  // role switch re-evaluates, but it never fights a manual bucket choice made
  // within the same role's dataset.
  useEffect(() => {
    if (loading || trades.length === 0 || autoBucketRoleRef.current === role) return;
    autoBucketRoleRef.current = role;
    if (bucket === 'active' && activeCount === 0 && doneCount > 0) setBucket('done');
  }, [loading, role, trades.length, activeCount, doneCount, bucket]);

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
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="title1" weight="700">
            My Trades
          </Text>
          <NotificationBell />
        </View>

        <View style={{ marginTop: spacing.md }}>
          <SegmentedControl
            segments={[
              { key: 'all', label: 'All' },
              { key: 'buyer', label: 'As buyer' },
              { key: 'seller', label: 'As seller' },
            ]}
            value={role}
            onChange={(k) => setRole(k as Role)}
          />
        </View>

        <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md, marginBottom: spacing.lg }}>
          {([
            { key: 'active', label: 'Active', count: activeCount },
            { key: 'done', label: 'Done', count: doneCount },
          ] as { key: Bucket; label: string; count: number }[]).map((b) => (
            <Chip
              key={b.key}
              label={b.count > 0 ? `${b.label} (${b.count})` : b.label}
              selected={bucket === b.key}
              onPress={() => setBucket(b.key)}
            />
          ))}
        </View>

        {loading || !userId ? (
          // Also wait for userId — the cards derive the viewer's role per-trade
          // from it; rendering before it resolves would mislabel buyer trades.
          <SkeletonGroup shape="tradeRow" count={4} />
        ) : visible.length === 0 ? (
          <EmptyState
            icon="swapVertical"
            title={bucket === 'active' ? 'No active trades' : 'No completed trades'}
            subtitle={
              role === 'all'
                ? bucket === 'active'
                  ? 'You have no in-flight trades.'
                  : 'No completed trades yet.'
                : bucket === 'active'
                ? `You have no in-flight trades as ${role}.`
                : `No completed trades as ${role} yet.`
            }
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {visible.map((t) => (
              <TradeCard key={t.id} trade={t} userId={userId} />
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const TradeCard: React.FC<{ trade: P2PTrade; userId?: string }> = ({ trade, userId }) => {
  const { colors, spacing } = useTheme();
  const decimals = TOKEN_DECIMALS[trade.token] ?? 18;
  const amount = formatTokenAmount(trade.tokenAmount, decimals, 4);
  // Derive the viewer's role per-trade (the list can mix both in the "All" view).
  const viewerRole: 'buyer' | 'seller' = trade.buyerId === userId ? 'buyer' : 'seller';
  const counterparty = viewerRole === 'buyer' ? trade.sellerId : trade.buyerId;

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
        <MetaRow label={viewerRole === 'buyer' ? 'Seller' : 'Buyer'}>
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
