// app/merchants/pay-links/index.tsx
//
// Payment links: both kinds of link a TSA Pay merchant has made, in the mode she
// is in (live once she can take live payments, test until then) — her reusable
// links first (the customer enters the amount; they stand, like a shop's QR),
// then her one-time links, newest first. The headline is what she checks
// through the day about one-time links — still waiting, and paid today (counted
// from the links loaded, and it says so once there are more). Opened with
// `?mode=` (a link just made in the other mode), it lists that mode.
// Kept fresh while she looks (every 15 s while focused); older pages load as
// she scrolls and stay put when the first page refreshes.
import React, { useCallback, useMemo, useRef, useState } from 'react';
import { FlatList, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Badge, Button, Card, EmptyState, ErrorState, getStatusVisual, PressableScale, Spinner, StatusPill, Text,
} from '@/components/ui';
import { TestLinkBanner } from '@/components/pay/PayLinkShare';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import {
  listOpenLinks, listPayLinks, payLinkModeOf, payLinkModeParam, type OpenLink, type PayLink, type PayLinkMode,
} from '@/services/payMerchantApi';
import { errorMessage } from '@/utils/payCheckout';
import {
  formatLinkTime, openLinkAmountRule, openLinkStatus, payLinkCounts, payLinkStatus,
} from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

// A page of each kind. Reusable links are few and stand for months: their first page is all of them in practice.
const PAGE = 50;
const POLL_MS = 15_000;

type Row = { kind: 'open'; link: OpenLink } | { kind: 'once'; link: PayLink };

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
  const [openLinks, setOpenLinks] = useState<OpenLink[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<string | undefined>();
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const pages = useRef(1);
  // Only the latest first-page read counts: a slow older one must not put the list back in time.
  const seq = useRef(0);

  const refresh = useCallback(async () => {
    const mine = ++seq.current;
    // Either kind failing leaves the other kind showing.
    const [once, open] = await Promise.allSettled([listPayLinks(mode, 1, PAGE), listOpenLinks(mode, 1, PAGE)]);
    setRefreshing(false);
    if (mine !== seq.current) return;
    if (once.status === 'fulfilled') {
      setLinks((shown) => mergeNewest(once.value.items, shown ?? []));
      setTotal(once.value.total);
    }
    if (open.status === 'fulfilled') setOpenLinks(open.value.items);
    const failed = once.status === 'rejected' ? once.reason : open.status === 'rejected' ? open.reason : null;
    setError(failed ? errorMessage(failed, 'Could not load your payment links.') : undefined);
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
  const rows = useMemo<Row[]>(() => [
    ...openLinks.map((link) => ({ kind: 'open' as const, link })),
    ...(links ?? []).map((link) => ({ kind: 'once' as const, link })),
  ], [openLinks, links]);

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
        data={rows}
        keyExtractor={(r) => `${r.kind}:${r.link.id}`}
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm }}
        ListHeaderComponent={header}
        ListEmptyComponent={
          <EmptyState icon="send" title="No payment links yet" subtitle="Request a payment and share the link with your customer." />
        }
        ListFooterComponent={loadingMore ? <Spinner /> : null}
        onEndReached={loadMore}
        onEndReachedThreshold={0.5}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => { setRefreshing(true); void refresh(); }} />}
        renderItem={({ item }) => (item.kind === 'open' ? <OpenLinkRow link={item.link} /> : <LinkRow link={item.link} />)}
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
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flexShrink: 1 }}>{link.description}</Text>
            <Badge label="One-time" size="sm" />
          </View>
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

function OpenLinkRow({ link }: { link: OpenLink }) {
  const { spacing } = useTheme();
  const status = openLinkStatus(link);
  const paid = `${link.paidCount} ${link.paidCount === 1 ? 'payment' : 'payments'}`;
  return (
    <PressableScale
      onPress={() => router.push(`/merchants/pay-links/open/${link.id}?mode=${link.mode}`)}
      accessibilityRole="button"
      accessibilityLabel={`${link.label}, reusable, ${paid}, ${getStatusVisual('openLink', status).label}`}
      testID={`open-link-${link.id}`}
    >
      <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
        <View style={{ flex: 1, gap: spacing.xs }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flexShrink: 1 }}>{link.label}</Text>
            <Badge label="Reusable" size="sm" />
          </View>
          <Text variant="caption" color="textSecondary" numberOfLines={1}>
            {`${openLinkAmountRule(link)} · ${paid}`}
          </Text>
        </View>
        <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
          {/* What reached her — the same figure as the link's screen ("You received …"). */}
          <Text variant="bodyMed" weight="700" numeric>{formatKobo(String(link.receivedTotal ?? 0))}</Text>
          <StatusPill kind="openLink" status={status} size="sm" />
        </View>
      </Card>
    </PressableScale>
  );
}
