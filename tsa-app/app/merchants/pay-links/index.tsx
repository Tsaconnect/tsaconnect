// app/merchants/pay-links/index.tsx
//
// Payment links: every link a TSA Pay merchant has sent, newest first, in the
// mode she is in (live once switched live, test until then). The headline is
// what she checks through the day — links still waiting, and links paid today
// (counted from the links loaded, and it says so once there are more). Opened
// with `?mode=` (a link just made in the other mode), it lists that mode.
// Kept fresh while she looks (every 15 s while focused); older pages load as
// she scrolls and stay put when the first page refreshes.
import React, { useCallback, useMemo, useRef, useState } from 'react';
import { FlatList, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button, Card, EmptyState, ErrorState, getStatusVisual, PressableScale, Spinner, StatusPill, Text,
} from '@/components/ui';
import { TestLinkBanner } from '@/components/pay/PayLinkShare';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import {
  listPayLinks, payLinkModeOf, payLinkModeParam, type PayLink, type PayLinkMode,
} from '@/services/payMerchantApi';
import { errorMessage } from '@/utils/payCheckout';
import { formatLinkTime, payLinkCounts, payLinkStatus } from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

const PAGE = 50;
const POLL_MS = 15_000;

/** Fresh rows win; older rows she has scrolled to are kept below them. */
const mergeNewest = (fresh: PayLink[], shown: PayLink[]) => {
  const ids = new Set(fresh.map((l) => l.id));
  return [...fresh, ...shown.filter((l) => !ids.has(l.id))];
};

export default function PayLinks() {
  const { load, reload } = useOwnPayMerchant();
  const modeParam = payLinkModeParam(useLocalSearchParams<{ mode?: string }>().mode);
  if (load.kind === 'loading') {
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }
  if (load.kind === 'error') {
    return <ErrorState title="Could not load your TSA Pay account" subtitle={load.message} onRetry={reload} />;
  }
  if (load.kind === 'none') {
    return <ErrorState title="No TSA Pay account" subtitle="Apply to accept payments with TSA Pay first." />;
  }
  const mode = modeParam ?? payLinkModeOf(load.merchant);
  // A new mode is a different list: nothing of the old one may stay on screen.
  return <LinkList key={mode} mode={mode} canRequest={load.merchant.status !== 'suspended'} />;
}

function LinkList({ mode, canRequest }: { mode: PayLinkMode; canRequest: boolean }) {
  const { colors, spacing } = useTheme();
  const [links, setLinks] = useState<PayLink[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<string | undefined>();
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const pages = useRef(1);
  // Only the latest first-page read counts: a slow older one must not put the list back in time.
  const seq = useRef(0);

  const refresh = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const page = await listPayLinks(mode, 1, PAGE);
      if (mine !== seq.current) return;
      setLinks((shown) => mergeNewest(page.items, shown ?? []));
      setTotal(page.total);
      setError(undefined);
    } catch (e) {
      if (mine !== seq.current) return;
      setError(errorMessage(e, 'Could not load your payment links.'));
    } finally {
      setRefreshing(false);
    }
  }, [mode]);

  useFocusPolling(refresh, POLL_MS);

  const loadMore = async () => {
    if (loadingMore || !links || links.length >= total) return;
    setLoadingMore(true);
    try {
      const page = await listPayLinks(mode, pages.current + 1, PAGE);
      pages.current += 1;
      setLinks((shown) => mergeNewest(shown ?? [], page.items));
    } catch {
      // The next scroll tries again; the rows she has stay.
    } finally {
      setLoadingMore(false);
    }
  };

  const counts = useMemo(() => payLinkCounts(links ?? []), [links]);

  if (links === null) {
    if (error) return <ErrorState title="Could not load your payment links" subtitle={error} onRetry={refresh} />;
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }

  const header = (
    <View style={{ gap: spacing.md, marginBottom: spacing.md }}>
      {mode === 'test' ? <TestLinkBanner /> : null}
      <View style={{ flexDirection: 'row', gap: spacing.sm }}>
        <Card style={{ flex: 1, gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">Awaiting payment</Text>
          <Text variant="title3" weight="700" numeric>{counts.awaiting}</Text>
        </Card>
        <Card style={{ flex: 1, gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">Paid today</Text>
          <Text variant="title3" weight="700" numeric>{counts.paidToday}</Text>
        </Card>
      </View>
      {total > links.length ? (
        <Text variant="caption" color="textMuted">{`Counted from your latest ${links.length} links.`}</Text>
      ) : null}
      {canRequest ? (
        <Button title="Request payment" leftIcon="send" onPress={() => router.push(`/merchants/pay-links/new?listMode=${mode}`)} />
      ) : null}
      {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}
    </View>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <FlatList
        data={links}
        keyExtractor={(l) => l.id}
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm }}
        ListHeaderComponent={header}
        ListEmptyComponent={
          <EmptyState icon="send" title="No payment links yet" subtitle="Request a payment and share the link with your customer." />
        }
        ListFooterComponent={loadingMore ? <Spinner /> : null}
        onEndReached={loadMore}
        onEndReachedThreshold={0.5}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => { setRefreshing(true); void refresh(); }} />}
        renderItem={({ item }) => <LinkRow link={item} />}
      />
    </SafeAreaView>
  );
}

function LinkRow({ link }: { link: PayLink }) {
  const { spacing } = useTheme();
  const status = payLinkStatus(link);
  return (
    <PressableScale
      onPress={() => router.push(`/merchants/pay-links/${link.id}?mode=${link.mode}`)}
      accessibilityRole="button"
      accessibilityLabel={`${link.description}, ${formatKobo(String(link.amount))}, ${getStatusVisual('payLink', status).label}`}
      testID={`pay-link-${link.id}`}
    >
      <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
        <View style={{ flex: 1, gap: spacing.xs }}>
          <Text variant="bodyMed" weight="700" numberOfLines={1}>{link.description}</Text>
          <Text variant="caption" color="textSecondary" numberOfLines={1}>
            {`${link.customerName || 'No customer name'} · ${formatLinkTime(link.createdAt)}`}
          </Text>
        </View>
        <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
          <Text variant="bodyMed" weight="700" numeric>{formatKobo(String(link.amount))}</Text>
          <StatusPill kind="payLink" status={status} size="sm" />
        </View>
      </Card>
    </PressableScale>
  );
}
