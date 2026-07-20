// components/spot/AssetPairSheet.tsx
//
// "Tapping any asset opens a selector for available pairs and chain network as
// it is listed" — one asset can be listed against several quote assets and on
// several chains, so tapping it offers the venues rather than guessing one.
//
// Venues arrive in the server's ranked order (see utils/spotPairs). Buy/Sell
// route through services/spotTrade, the same rule the Spot list uses.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { EmptyState, PressableScale, Sheet, Text } from '@/components/ui';
import type { SpotListing } from '@/services/spotApi';
import { openSpotTrade } from '@/services/spotTrade';

export interface AssetPairSheetProps {
  visible: boolean;
  onClose: () => void;
  /** Base symbol that was tapped, e.g. "ETH". */
  symbol: string;
  /** Listings for that symbol, already filtered (see listingsForSymbol). */
  listings: SpotListing[];
}

export const AssetPairSheet: React.FC<AssetPairSheetProps> = ({
  visible,
  onClose,
  symbol,
  listings,
}) => {
  const { colors, spacing, radius } = useTheme();

  const trade = (listing: SpotListing, side: 'buy' | 'sell') => {
    onClose(); // close first so the desk isn't pushed behind an open sheet
    openSpotTrade(listing, side);
  };

  const tradeBtn = (listing: SpotListing, side: 'buy' | 'sell') => (
    <PressableScale
      onPress={() => trade(listing, side)}
      accessibilityRole="button"
      accessibilityLabel={`${side} ${listing.baseSymbol} for ${listing.quoteSymbol} on ${listing.chain}`}
      style={{
        paddingVertical: spacing.xs,
        paddingHorizontal: spacing.md,
        borderRadius: radius.md,
        backgroundColor: side === 'buy' ? colors.success : colors.danger,
        alignItems: 'center',
        minWidth: 58,
      }}
    >
      <Text variant="bodySm" weight="700" style={{ color: '#fff' }}>
        {side === 'buy' ? 'Buy' : 'Sell'}
      </Text>
    </PressableScale>
  );

  return (
    <Sheet visible={visible} onClose={onClose} title={`Trade ${symbol}`} scrollable>
      <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg }}>
        {listings.length === 0 ? (
          <EmptyState
            icon="clock"
            title="Not listed for trading yet"
            subtitle={`${symbol} has no trading pairs available. Pairs are added by the team.`}
          />
        ) : (
          <>
            <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.md }}>
              {listings.length === 1
                ? 'Available pair'
                : `${listings.length} available pairs`}
            </Text>
            {listings.map((l) => (
              <View
                key={l.id}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingVertical: spacing.md,
                  borderBottomWidth: 1,
                  borderBottomColor: colors.border,
                }}
              >
                <View style={{ flex: 1, gap: 2 }}>
                  <Text variant="bodyMed" weight="700">
                    {l.baseSymbol}/{l.quoteSymbol}
                  </Text>
                  <View
                    style={{
                      alignSelf: 'flex-start',
                      paddingHorizontal: spacing.xs,
                      paddingVertical: 1,
                      borderRadius: radius.sm,
                      backgroundColor: colors.surfaceAlt,
                    }}
                  >
                    <Text variant="caption" color="textMuted">
                      {l.chain}
                    </Text>
                  </View>
                </View>
                <View style={{ flexDirection: 'row', gap: spacing.xs }}>
                  {tradeBtn(l, 'buy')}
                  {tradeBtn(l, 'sell')}
                </View>
              </View>
            ))}
          </>
        )}
      </View>
    </Sheet>
  );
};

export default AssetPairSheet;
