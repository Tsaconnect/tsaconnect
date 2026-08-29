// components/otc/OtcListingList.tsx
//
// The OTC tab's catalogue: Direct | Presale sub-tabs over a listing table of
// Name / Asset Class / Trade, with green Buy and red Sell per row.
//
// This replaces jumping straight into the MCGP/USDC form. The desk is now a
// row you choose, which is what makes the admin's listing metadata mean
// anything: the logo and asset class have somewhere to render, and Category
// decides which sub-tab the row sits under. It also gives the pause switches a
// visible home — a paused direction dims its button in the list, so the user
// sees the state before committing to a form.
//
// Row chrome deliberately mirrors components/spot/TokenMarketList (logo with a
// letter-badge fallback) and AssetPairSheet (the Buy/Sell pills), because Spot
// is the pattern this screen is meant to read as a sibling of.
import React, { useCallback, useState } from 'react';
import { View, Image, ScrollView, RefreshControl } from 'react-native';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { useTheme } from '@/theme';
import {
  Badge, Divider, EmptyState, PressableScale, SegmentedControl, Spinner, Surface, Text,
} from '@/components/ui';
import { getOtcSettings, type OtcSettings } from '@/services/walletApi';
import {
  otcListingsFrom, listingsForCategory, canTrade,
  type OtcCategory, type OtcListing,
} from '@/utils/otcListings';
import { OtcClosedSheet } from './OtcClosedSheet';

const CATEGORY_TABS: { key: OtcCategory; label: string }[] = [
  { key: 'direct', label: 'Direct' },
  { key: 'presale', label: 'Presale' },
];

/** Row avatar edge; the column header reserves the same width to stay aligned. */
const AVATAR = 36;
/**
 * Fixed column widths, shared by the rows and the header so they line up.
 *
 * Name is FIXED rather than flexed: the pair is the row's identity and must
 * never truncate. Two equal flex columns gave it 72dp on a 360dp screen, and
 * "MCGP/USDC" at 16pt bold needs ~88dp — it rendered as "MCGP/U…". The asset
 * class takes the remainder instead, since a class code is short and is the
 * safer thing to squeeze.
 */
const NAME_COL = 104;
const TRADE_COL = 104;

const TradePill = ({
  side, enabled, onPress,
}: { side: 'buy' | 'sell'; enabled: boolean; onPress: () => void }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={`${side === 'buy' ? 'Buy' : 'Sell'}${enabled ? '' : ' — currently closed'}`}
      accessibilityState={{ disabled: !enabled }}
      style={{
        paddingVertical: spacing.xs,
        paddingHorizontal: spacing.md,
        borderRadius: radius.md,
        backgroundColor: side === 'buy' ? colors.success : colors.danger,
        alignItems: 'center',
        minWidth: 48,
        // Dimmed, not removed: the button stays pressable so the tap can
        // explain why it is closed.
        opacity: enabled ? 1 : 0.4,
      }}
    >
      <Text variant="bodySm" weight="700" style={{ color: '#fff' }}>
        {side === 'buy' ? 'Buy' : 'Sell'}
      </Text>
    </PressableScale>
  );
};

const ListingRow = ({
  listing, onTrade,
}: { listing: OtcListing; onTrade: (side: 'buy' | 'sell') => void }) => {
  const { colors, spacing, radius } = useTheme();
  // Admin-entered logo URLs can be share-page links or dead — fall back to the
  // letter badge rather than rendering a blank circle.
  const [logoFailed, setLogoFailed] = useState(false);

  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
      }}
    >
      {listing.logoUrl && !logoFailed ? (
        <Image
          source={{ uri: listing.logoUrl }}
          onError={() => setLogoFailed(true)}
          style={{
            width: AVATAR, height: AVATAR,
            borderRadius: radius.pill,
            backgroundColor: colors.surfaceAlt,
          }}
        />
      ) : (
        <View
          style={{
            width: AVATAR, height: AVATAR,
            borderRadius: radius.pill,
            borderWidth: 2,
            borderColor: colors.accent,
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <Text variant="bodyMed" weight="700" color="accent">
            {listing.baseSymbol.charAt(0)}
          </Text>
        </View>
      )}

      <View style={{ width: NAME_COL }}>
        <Text variant="bodyMed" weight="700" numberOfLines={1}>
          {listing.pair}
        </Text>
      </View>

      <View style={{ flex: 1, alignItems: 'flex-start' }}>
        {listing.assetClass ? (
          <Badge
            label={listing.assetClass}
            size="sm"
            color={colors.accent}
            background={colors.accentMuted}
          />
        ) : (
          <Text variant="caption" color="textMuted">—</Text>
        )}
      </View>

      <View style={{ width: TRADE_COL, flexDirection: 'row', gap: spacing.xs, justifyContent: 'flex-end' }}>
        <TradePill side="buy" enabled={listing.buyEnabled} onPress={() => onTrade('buy')} />
        <TradePill side="sell" enabled={listing.sellEnabled} onPress={() => onTrade('sell')} />
      </View>
    </View>
  );
};

const ColumnHeader = () => {
  const { spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingHorizontal: spacing.lg,
        paddingBottom: spacing.sm,
      }}
    >
      <View style={{ width: AVATAR }} />
      <Text variant="overline" color="textMuted" style={{ width: NAME_COL }}>Name</Text>
      <Text variant="overline" color="textMuted" style={{ flex: 1 }}>Asset Class</Text>
      <Text variant="overline" color="textMuted" style={{ width: TRADE_COL, textAlign: 'right' }}>
        Trade
      </Text>
    </View>
  );
};

const OtcListingList = () => {
  const { colors, spacing } = useTheme();
  const [tab, setTab] = useState<OtcCategory>('direct');
  const [settings, setSettings] = useState<OtcSettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  // Which direction's closed-notice is open, if any.
  const [closedSide, setClosedSide] = useState<'buy' | 'sell' | null>(null);

  const load = useCallback(async () => {
    const s = await getOtcSettings();
    setSettings(s);
    setLoading(false);
    setRefreshing(false);
  }, []);

  // Refetch on focus so an admin pause takes effect without an app restart
  // (getOtcSettings caches for a minute of its own accord).
  useFocusEffect(useCallback(() => { load(); }, [load]));

  const visible = listingsForCategory(otcListingsFrom(settings), tab);

  const onTrade = (listing: OtcListing, side: 'buy' | 'sell') => {
    if (!canTrade(listing, side)) {
      setClosedSide(side);
      return;
    }
    router.push(`/otc-trade?side=${side}`);
  };

  return (
    <View style={{ flex: 1 }}>
      <View style={{ paddingHorizontal: spacing.xl, paddingBottom: spacing.lg }}>
        <SegmentedControl
          segments={CATEGORY_TABS}
          value={tab}
          onChange={(k) => setTab(k as OtcCategory)}
        />
      </View>

      {loading ? (
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner label="Loading OTC listings…" />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ paddingBottom: spacing['4xl'] }}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={() => { setRefreshing(true); load(); }}
              tintColor={colors.accent}
              colors={[colors.accent]}
            />
          }
        >
          {visible.length === 0 ? (
            <View style={{ paddingTop: spacing['4xl'] }}>
              <EmptyState
                icon="clock"
                title={tab === 'presale' ? 'No active presale' : 'No direct listings'}
                subtitle={
                  tab === 'presale'
                    ? 'Presale listings will appear here once projects go live.'
                    : 'Direct listings will appear here once the team lists them.'
                }
              />
            </View>
          ) : (
            <Surface>
              <ColumnHeader />
              {visible.map((l, i) => (
                <View key={l.id}>
                  {i > 0 ? <Divider /> : null}
                  <ListingRow listing={l} onTrade={(side) => onTrade(l, side)} />
                </View>
              ))}
            </Surface>
          )}
        </ScrollView>
      )}

      <OtcClosedSheet side={closedSide} onClose={() => setClosedSide(null)} />
    </View>
  );
};

export default OtcListingList;
