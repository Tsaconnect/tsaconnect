// components/spot/SpotList.tsx
//
// The Spot Trade pane: the admin-curated catalogue rendered as TOKENS (one row
// per asset, not per pair) with live price + 24h change. Tapping a token opens
// AssetPairSheet, which lists every pair/chain it trades on and routes Buy/Sell
// to the EXISTING flows by `route` — MCGP → the Contract B USDC OTC desk,
// generic → the Spot Trade desk (app/wallet/spot-trade, LiFi under the hood).
// No new swap logic lives here.
import React, { useCallback, useMemo, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { useTheme } from '@/theme';
import { EmptyState, Spinner } from '@/components/ui';
import { useSpotListings } from '@/hooks/useSpotListings';
import { type SpotTabKey } from '@/utils/spotTabs';
import { buildTokenMarkets, tokensForTab } from '@/utils/spotTokens';
import { listingsForSymbol } from '@/utils/spotPairs';
import { useSpotFavorites } from '@/hooks/useSpotFavorites';
import AssetPairSheet from '@/components/spot/AssetPairSheet';
import TokenMarketList from '@/components/spot/TokenMarketList';

const SpotList = () => {
  const { colors, spacing } = useTheme();
  // Shared cache — the wallet filter and homepage list read the same dataset.
  const { listings, loading, error, reload } = useSpotListings();
  const [refreshing, setRefreshing] = useState(false);
  const [tab, setTab] = useState<SpotTabKey>('all');
  const [pickerSymbol, setPickerSymbol] = useState<string | null>(null);
  const { favoriteSymbols, toggleFavorite } = useSpotFavorites();

  const tokens = useMemo(() => buildTokenMarkets(listings), [listings]);
  const visible = useMemo(
    () => tokensForTab(tokens, tab, favoriteSymbols),
    [tokens, tab, favoriteSymbols],
  );

  // Venues for the tapped asset — every pair/chain it's listed on.
  const pickerListings = useMemo(
    () => (pickerSymbol ? listingsForSymbol(listings, pickerSymbol) : []),
    [listings, pickerSymbol],
  );

  const load = useCallback(async () => {
    await reload();
    setRefreshing(false);
  }, [reload]);

  if (loading) {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
        <Spinner label="Loading spot markets…" />
      </View>
    );
  }

  if (error) {
    return (
      <View style={{ flex: 1, justifyContent: 'center' }}>
        <EmptyState icon="closeCircle" title="Couldn’t load spot markets" subtitle={error} />
      </View>
    );
  }

  if (listings.length === 0) {
    return (
      <View style={{ flex: 1, justifyContent: 'center' }}>
        <EmptyState
          icon="clock"
          title="No spot markets yet"
          subtitle="Check back soon — new tokens are listed by the team."
        />
      </View>
    );
  }

  return (
    <View style={{ flex: 1 }}>
      <ScrollView
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg }}
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
        <TokenMarketList
          tokens={visible}
          tab={tab}
          onTabChange={setTab}
          favoriteSymbols={favoriteSymbols}
          onToggleFavorite={toggleFavorite}
          onTokenPress={setPickerSymbol}
          style={{ marginTop: spacing.md }}
        />
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
