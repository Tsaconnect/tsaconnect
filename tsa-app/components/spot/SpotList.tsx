// components/spot/SpotList.tsx
//
// The Spot Trade pane: renders the admin-curated list of pairs (services/spotApi)
// with live price + 24h change, and Buy/Sell buttons that route to EXISTING flows
// by `route` — MCGP → the Contract B USDC OTC desk, generic → the Spot Trade
// desk (app/wallet/spot-trade, LiFi under the hood). No new swap logic lives here.
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView, RefreshControl, Image } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Card, EmptyState, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import { getSpotListings, type SpotListing } from '@/services/spotApi';

// USD formatter that stays readable across BTC-scale and sub-cent prices.
function formatUsd(v?: number | null): string {
  if (v == null || !isFinite(v)) return '—';
  if (v >= 1) {
    return `$${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  }
  return `$${v.toPrecision(3).replace(/\.?0+$/, '')}`;
}

// Deterministic accent for the letter badge when a listing has no logo URL.
function badgeColor(symbol: string, palette: string[]): string {
  let hash = 0;
  for (let i = 0; i < symbol.length; i++) hash = (hash * 31 + symbol.charCodeAt(i)) >>> 0;
  return palette[hash % palette.length];
}

function openTrade(listing: SpotListing, side: 'buy' | 'sell') {
  if (listing.route === 'mcgp') {
    router.push(side === 'buy' ? '/wallet/mcgp/buy' : '/wallet/mcgp/sell');
    return;
  }
  // Generic tokens open the Spot Trade desk — MCGP-style form with both legs'
  // balances, LiFi under the hood (see app/wallet/spot-trade).
  router.push({
    pathname: '/wallet/spot-trade',
    params: {
      side,
      base: listing.baseSymbol,
      quote: listing.quoteSymbol,
      chain: listing.chain,
    },
  } as any);
}

const SpotRow = ({ listing }: { listing: SpotListing }) => {
  const { colors, spacing, radius } = useTheme();
  const hasChange = listing.change24h != null;
  const isUp = (listing.change24h ?? 0) >= 0;
  const changeColor = isUp ? colors.success : colors.danger;
  const changeBg = isUp ? colors.successBg : colors.dangerBg;
  const dotColor = badgeColor(listing.baseSymbol, [colors.accent, colors.success, colors.danger]);

  const tradeBtn = (side: 'buy' | 'sell') => (
    <PressableScale
      onPress={() => openTrade(listing, side)}
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
        {/* Logo / letter badge */}
        {listing.logoUrl ? (
          <Image
            source={{ uri: listing.logoUrl }}
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

        {/* Buy / Sell */}
        <View style={{ gap: spacing.xs }}>
          {tradeBtn('buy')}
          {tradeBtn('sell')}
        </View>
      </View>
    </Card>
  );
};

const SpotList = () => {
  const { colors, spacing } = useTheme();
  const [listings, setListings] = useState<SpotListing[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    const r = await getSpotListings();
    if (r.success && r.data) {
      setListings(r.data.listings ?? []);
      setError('');
    } else {
      setError(r.message || 'Could not load spot pairs');
    }
    setLoading(false);
    setRefreshing(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

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

  return (
    <ScrollView
      contentContainerStyle={{ padding: spacing.lg }}
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
      {listings.map((l) => (
        <SpotRow key={l.id} listing={l} />
      ))}
    </ScrollView>
  );
};

export default SpotList;
