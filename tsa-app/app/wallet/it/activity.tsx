// app/wallet/it/activity.tsx
//
// Every Instant Transfer order, across both systems behind it (IT intents and
// Paycrest orders), newest first. The hub shows the latest few; this is where
// "View all" lands.
//
// Unlike the hub, cancelled buys are shown — this is the record, and a user
// looking for an order they abandoned should find it here.
//
// Both list endpoints return at most 100 rows and neither pages, so the list is
// the latest 100 of each and says so rather than implying it is complete.
import React, { useCallback, useMemo, useState } from 'react';
import { FlatList, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect } from '@react-navigation/native';
import { useTheme } from '@/theme';
import {
  Card,
  Divider,
  EmptyState,
  ErrorState,
  SegmentedControl,
  Skeleton,
  Text,
} from '@/components/ui';
import ITActivityRow, { ACTIVITY_DIVIDER_INSET } from '@/components/it/ITActivityRow';
import { listITIntents, type ITIntent } from '@/services/itApi';
import { listOrders as listPaycrestOrders, type PaycrestOrder } from '@/services/paycrestApi';
import { useFiatSymbol } from '@/hooks/useFiatSymbol';
import { buildITActivity } from '@/utils/itActivity';

const LIST_CAP = 100;

// Module-level so FlatList does not remount every separator on each render.
const Separator = () => <Divider inset={ACTIVITY_DIVIDER_INSET} />;

type Filter = 'all' | 'buy' | 'sell';
const FILTERS: { key: Filter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'buy', label: 'Buy' },
  { key: 'sell', label: 'Sell' },
];

export default function ITActivityScreen() {
  const { colors, spacing } = useTheme();
  const symbolFor = useFiatSymbol();

  const [intents, setIntents] = useState<ITIntent[]>([]);
  const [orders, setOrders] = useState<PaycrestOrder[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  // Per source, because this screen is the record: a list quietly missing
  // half its orders would read as "that order does not exist".
  const [intentsFailed, setIntentsFailed] = useState(false);
  const [ordersFailed, setOrdersFailed] = useState(false);
  const [intentsError, setIntentsError] = useState('');
  const [filter, setFilter] = useState<Filter>('all');

  const load = useCallback(async () => {
    const [list, pc] = await Promise.all([
      listITIntents().catch(() => null),
      listPaycrestOrders(LIST_CAP).catch(() => null),
    ]);
    const intentsOk = !!(list?.success && list.data?.intents);
    if (intentsOk) setIntents(list!.data!.intents);
    setIntentsFailed(!intentsOk);
    setIntentsError(intentsOk ? '' : list?.message || '');
    if (pc) setOrders(pc);
    setOrdersFailed(!pc);
    setLoading(false);
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }, [load]);

  const rows = useMemo(() => {
    const all = buildITActivity(intents, orders, symbolFor);
    return filter === 'all' ? all : all.filter((r) => r.direction === filter);
  }, [intents, orders, symbolFor, filter]);

  const capped = intents.length >= LIST_CAP || orders.length >= LIST_CAP;

  // An IT failure always gets a notice. A Paycrest failure on its own does not:
  // Paycrest is dormant on servers without its key, and that is not an error.
  // (Both failing with nothing to show is the full-screen error below.)
  const partialFailure = !loading && intentsFailed;

  if (!loading && intentsFailed && ordersFailed && intents.length + orders.length === 0) {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ErrorState
          title="Could not load activity"
          subtitle={intentsError || 'Check your connection and try again.'}
          onRetry={load}
        />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <FlatList
        data={loading ? [] : rows}
        keyExtractor={(r) => r.key}
        renderItem={({ item }) => <ITActivityRow item={item} />}
        ItemSeparatorComponent={Separator}
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
        }
        ListHeaderComponent={
          <View style={{ gap: spacing.md, marginVertical: spacing.md }}>
            <SegmentedControl segments={FILTERS} value={filter} onChange={setFilter} />
            {partialFailure ? (
              <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
                <Text variant="bodySm" style={{ color: colors.warning }}>
                  Some orders could not be loaded. Pull down to try again.
                </Text>
              </Card>
            ) : null}
          </View>
        }
        ListEmptyComponent={
          loading ? (
            <View style={{ gap: spacing.md, paddingTop: spacing.sm }}>
              {[0, 1, 2, 3, 4].map((k) => (
                <Skeleton key={k} height={44} radius="md" />
              ))}
            </View>
          ) : (
            <EmptyState
              icon="swap"
              title={filter === 'all' ? 'No activity yet' : `No ${filter} orders`}
              subtitle="Orders you open from Instant Transfer show up here."
            />
          )
        }
        ListFooterComponent={
          !loading && rows.length > 0 ? (
            <Text
              variant="caption"
              align="center"
              style={{ color: colors.textMuted, paddingVertical: spacing.lg }}
            >
              {capped ? 'Showing your most recent orders' : 'All caught up'}
            </Text>
          ) : null
        }
      />
    </SafeAreaView>
  );
}
