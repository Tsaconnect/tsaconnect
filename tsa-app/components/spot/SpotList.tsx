// components/spot/SpotList.tsx
//
// The Spot Trade pane: renders the admin-curated list of pairs (services/spotApi)
// with live price + 24h change, and Buy/Sell buttons that route to EXISTING flows
// by `route` — MCGP → the Contract B USDC OTC desk, generic → the Spot Trade
// desk (app/wallet/spot-trade, LiFi under the hood). No new swap logic lives here.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView, RefreshControl, Image } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Chip, EmptyState, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import { type SpotListing } from '@/services/spotApi';
import { useSpotListings } from '@/hooks/useSpotListings';
import { formatMicroPrice, isMicroPrice } from '@/utils/formatPrice';
import { filterListingsByTab, SPOT_TABS, type SpotTabKey } from '@/utils/spotTabs';
import { listingsForSymbol } from '@/utils/spotPairs';
import { openSpotTrade } from '@/services/spotTrade';
import { useSpotFavorites } from '@/hooks/useSpotFavorites';
import AssetPairSheet from '@/components/spot/AssetPairSheet';

// USD formatter that stays readable across BTC-scale and sub-cent prices.
function formatUsd(v?: number | null): string {
  if (v == null || !isFinite(v)) return '—';
  if (v >= 1) {
    return `$${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  }
  // Sub-cent (micro-cap) prices delegate to the shared leading-zero
  // compression: $0.0000012 → "$0.0₅12".
  if (isMicroPrice(v)) return `$${formatMicroPrice(v)}`;
  return `$${v.toPrecision(3).replace(/\.?0+$/, '')}`;
}

// Deterministic accent for the letter badge when a listing has no logo URL.
function badgeColor(symbol: string, palette: string[]): string {
  let hash = 0;
  for (let i = 0; i < symbol.length; i++) hash = (hash * 31 + symbol.charCodeAt(i)) >>> 0;
  return palette[hash % palette.length];
}

const SpotRow = ({
  listing,
  isFavorite,
  onToggleFavorite,
  onPress,
}: {
  listing: SpotListing;
  isFavorite: boolean;
  onToggleFavorite: () => void;
  onPress: () => void;
}) => {
  const { colors, spacing, radius } = useTheme();
  // Admin-entered logo URLs can be share-page links (ibb.co/…) or dead — fall
  // back to the letter badge instead of rendering a blank circle.
  const [logoFailed, setLogoFailed] = useState(false);
  const hasChange = listing.change24h != null;
  const isUp = (listing.change24h ?? 0) >= 0;
  const changeColor = isUp ? colors.success : colors.danger;
  const changeBg = isUp ? colors.successBg : colors.dangerBg;
  const dotColor = badgeColor(listing.baseSymbol, [colors.accent, colors.success, colors.danger]);

  const tradeBtn = (side: 'buy' | 'sell') => (
    <PressableScale
      onPress={() => openSpotTrade(listing, side)}
      accessibilityRole="button"
      accessibilityLabel={`${side} ${listing.baseSymbol}`}
      style={{
        paddingVertical: spacing.xs,
        paddingHorizontal: spacing.md,
        borderRadius: radius.md,
        backgroundColor: side === 'buy' ? colors.success : colors.danger,
        alignItems: 'center',
        minWidth: 60,
      }}
    >
      <Text variant="bodySm" weight="700" style={{ color: '#fff' }}>
        {side === 'buy' ? 'Buy' : 'Sell'}
      </Text>
    </PressableScale>
  );

  return (
    <Card padding="md" style={{ marginBottom: spacing.md }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
        {/* Tapping the asset itself opens the pair/chain picker; Buy/Sell and
            the favourite toggle stay direct actions. */}
        <PressableScale
          onPress={onPress}
          accessibilityRole="button"
          accessibilityLabel={`View ${listing.baseSymbol} pairs and chains`}
          style={{ flex: 1, flexDirection: 'row', alignItems: 'center', gap: spacing.md }}
        >
        {/* Logo / letter badge */}
        {listing.logoUrl && !logoFailed ? (
          <Image
            source={{ uri: listing.logoUrl }}
            onError={() => setLogoFailed(true)}
            style={{ width: 40, height: 40, borderRadius: radius.pill, backgroundColor: colors.surfaceAlt }}
          />
        ) : (
          <View
            style={{
              width: 40,
              height: 40,
              borderRadius: radius.pill,
              borderWidth: 2,
              borderColor: dotColor,
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <Text variant="bodyMed" weight="700" style={{ color: dotColor }}>
              {listing.baseSymbol.charAt(0)}
            </Text>
          </View>
        )}

        {/* Pair + chain + price */}
        <View style={{ flex: 1, gap: 2 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, flexWrap: 'wrap' }}>
            <Text variant="bodyMed" weight="700">
              {listing.baseSymbol}/{listing.quoteSymbol}
            </Text>
            <View
              style={{
                paddingHorizontal: spacing.xs,
                paddingVertical: 1,
                borderRadius: radius.sm,
                backgroundColor: colors.surfaceAlt,
              }}
            >
              <Text variant="caption" color="textMuted">
                {listing.chain}
              </Text>
            </View>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Text variant="bodySm" weight="600">
              {formatUsd(listing.price)}
            </Text>
            {hasChange && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: 2,
                  paddingHorizontal: spacing.xs,
                  paddingVertical: 1,
                  borderRadius: radius.sm,
                  backgroundColor: changeBg,
                }}
              >
                <Icon name={isUp ? 'trendingUp' : 'trendingDown'} size={12} color={changeColor} />
                <Text variant="caption" weight="600" style={{ color: changeColor }}>
                  {`${isUp ? '+' : ''}${(listing.change24h ?? 0).toFixed(2)}%`}
                </Text>
              </View>
            )}
          </View>
        </View>
        </PressableScale>

        {/* Favorite toggle */}
        <PressableScale
          onPress={onToggleFavorite}
          accessibilityRole="button"
          accessibilityLabel={isFavorite ? `Remove ${listing.baseSymbol} from favorites` : `Add ${listing.baseSymbol} to favorites`}
          accessibilityState={{ selected: isFavorite }}
          hitSlop={8}
          style={{ padding: spacing.xs }}
        >
          <Icon
            name={isFavorite ? 'heart' : 'heartOutline'}
            size={20}
            color={isFavorite ? colors.danger : colors.textMuted}
          />
        </PressableScale>

        {/* Buy / Sell */}
        <View style={{ gap: spacing.xs }}>
          {tradeBtn('buy')}
          {tradeBtn('sell')}
        </View>
      </View>
    </Card>
  );
};

// Per-tab empty-state copy so a filtered-empty tab doesn't read like an outage.
const TAB_EMPTY: Partial<Record<SpotTabKey, { title: string; subtitle: string }>> = {
  favorites: { title: 'No favorites yet', subtitle: 'Tap the heart on any pair to pin it here.' },
  new: { title: 'No new listings', subtitle: 'Pairs added in the last two weeks show up here.' },
  gainers: { title: 'No gainers right now', subtitle: 'Pairs trading up over 24h show up here.' },
  losers: { title: 'No losers right now', subtitle: 'Pairs trading down over 24h show up here.' },
};

const SpotList = () => {
  const { colors, spacing } = useTheme();
  // Shared cache — the wallet filter and homepage picker read the same dataset.
  const { listings, loading, error, reload } = useSpotListings();
  const [refreshing, setRefreshing] = useState(false);
  const [tab, setTab] = useState<SpotTabKey>('all');
  const [pickerSymbol, setPickerSymbol] = useState<string | null>(null);
  const { favoriteIds, toggleFavorite } = useSpotFavorites();

  // Venues for the tapped asset — every pair/chain it's listed on, not just
  // the row that was tapped.
  const pickerListings = useMemo(
    () => (pickerSymbol ? listingsForSymbol(listings, pickerSymbol) : []),
    [listings, pickerSymbol],
  );

  const visible = useMemo(
    () => filterListingsByTab(listings, tab, favoriteIds),
    [listings, tab, favoriteIds],
  );

  const load = useCallback(async () => {
    await reload();
    setRefreshing(false);
  }, [reload]);


  if (loading) {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
        <Spinner label="Loading spot pairs…" />
      </View>
    );
  }

  if (error) {
    return (
      <View style={{ flex: 1, justifyContent: 'center' }}>
        <EmptyState icon="closeCircle" title="Couldn’t load spot pairs" subtitle={error} />
      </View>
    );
  }

  if (listings.length === 0) {
    return (
      <View style={{ flex: 1, justifyContent: 'center' }}>
        <EmptyState icon="clock" title="No spot pairs yet" subtitle="Check back soon — new pairs are added by the team." />
      </View>
    );
  }

  const emptyTab = TAB_EMPTY[tab] ?? {
    title: 'Nothing in this tab yet',
    subtitle: 'Pairs assigned to this category will show up here.',
  };

  return (
    <View style={{ flex: 1 }}>
      {/* Category tabs (taxonomy + computed) */}
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={{
          gap: spacing.xs,
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: spacing.xs,
        }}
      >
        {SPOT_TABS.map((t) => (
          <Chip
            key={t.key}
            label={t.label}
            leftIcon={t.key === 'favorites' ? 'heart' : t.key === 'hot' ? 'trendingUp' : undefined}
            selected={tab === t.key}
            onPress={() => setTab(t.key)}
          />
        ))}
      </ScrollView>

      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, flexGrow: 1 }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
            tintColor={colors.accent}
          />
        }
      >
        {visible.length === 0 ? (
          <View style={{ flex: 1, justifyContent: 'center' }}>
            <EmptyState icon="clock" title={emptyTab.title} subtitle={emptyTab.subtitle} />
          </View>
        ) : (
          visible.map((l) => (
            <SpotRow
              key={l.id}
              listing={l}
              isFavorite={favoriteIds.has(l.id)}
              onToggleFavorite={() => toggleFavorite(l.id)}
              onPress={() => setPickerSymbol(l.baseSymbol)}
            />
          ))
        )}
      </ScrollView>

      <AssetPairSheet
        visible={pickerSymbol != null}
        onClose={() => setPickerSymbol(null)}
        symbol={pickerSymbol ?? ''}
        listings={pickerListings}
        error={error ?? undefined}
        onRetry={reload}
      />
    </View>
  );
};

export default SpotList;
