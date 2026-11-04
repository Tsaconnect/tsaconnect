// components/spot/TokenMarketList.tsx
//
// The shared token market list — one row per TOKEN (never per pair), with the
// spot taxonomy tabs above it. Home renders a capped preview of it, Spot Trade
// renders the whole catalogue; tapping a row opens AssetPairSheet, which is
// where the pairs and the Buy/Sell buttons live.
//
// Rows are laid out like an exchange listing: Name / Last Price / 24h chg%.
// No balances — holdings live on the Wallet tab.
//
// This component owns NO vertical scroll view: Home mounts it inside the
// dashboard scroller and Spot Trade inside its own pull-to-refresh scroller,
// and nesting vertical scrollers breaks both.
import React from 'react';
import { View, ScrollView, type StyleProp, type ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Chip, Divider, EmptyState, Icon, PressableScale, Surface, Text } from '@/components/ui';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatMicroPrice, isMicroPrice } from '@/utils/formatPrice';
import { SPOT_TABS, type SpotTabKey } from '@/utils/spotTabs';
import type { TokenMarket } from '@/utils/spotTokens';
import TokenAvatar from '@/components/wallet/TokenAvatar';

// USD formatter that stays readable across BTC-scale and sub-cent prices.
// The currency context's own formatter rounds to 2dp, which renders every
// micro-cap token as "$0.00", so prices format here and only the secondary
// fiat line comes from the context.
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

/** Row avatar edge; the column header reserves the same width to stay aligned. */
const AVATAR_SIZE = 36;
/** Change-chip column width, shared by the rows and the column header. */
const CHANGE_COL_WIDTH = 68;

// Per-tab empty copy so a filtered-empty tab doesn't read like an outage.
const TAB_EMPTY: Partial<Record<SpotTabKey, { title: string; subtitle: string }>> = {
  favorites: { title: 'No favorites yet', subtitle: 'Tap the heart on any token to pin it here.' },
  new: { title: 'No new listings', subtitle: 'Tokens listed in the last two weeks show up here.' },
  gainers: { title: 'No gainers right now', subtitle: 'Tokens trading up over 24h show up here.' },
  losers: { title: 'No losers right now', subtitle: 'Tokens trading down over 24h show up here.' },
};

const TokenRow = ({
  token,
  isFavorite,
  onToggleFavorite,
  onPress,
}: {
  token: TokenMarket;
  isFavorite: boolean;
  onToggleFavorite: () => void;
  onPress: () => void;
}) => {
  const { colors, spacing, radius } = useTheme();
  const { formatDualPrice } = useCurrency();

  const hasChange = token.change24h != null;
  const isUp = (token.change24h ?? 0) >= 0;
  const changeColor = isUp ? colors.success : colors.danger;
  const changeBg = isUp ? colors.successBg : colors.dangerBg;

  return (
    <PressableScale
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={`View ${token.symbol} pairs and chains`}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
      }}
    >
      {/* Admin-entered logo URLs can be share-page links or dead; TokenAvatar
          falls back to the registry logo, then a letter. */}
      <TokenAvatar symbol={token.symbol} iconUrl={token.logoUrl} size={AVATAR_SIZE} />

      {/* Name column — symbol, favourite toggle, and how it's listed */}
      <View style={{ flex: 1, gap: 2 }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Text variant="bodySm" weight="700">
            {token.symbol}
          </Text>
          {/* The heart sits inside the name column so the row stays three
              columns wide on small screens. */}
          <PressableScale
            onPress={onToggleFavorite}
            accessibilityRole="button"
            accessibilityLabel={
              isFavorite
                ? `Remove ${token.symbol} from favorites`
                : `Add ${token.symbol} to favorites`
            }
            accessibilityState={{ selected: isFavorite }}
            hitSlop={10}
          >
            <Icon
              name={isFavorite ? 'heart' : 'heartOutline'}
              size={14}
              color={isFavorite ? colors.danger : colors.textMuted}
            />
          </PressableScale>
        </View>
        <Text variant="caption" color="textMuted">
          {token.subtitle}
        </Text>
      </View>

      {/* Last price */}
      <View style={{ alignItems: 'flex-end', gap: 2 }}>
        <Text variant="bodySm" weight="700" numeric>
          {formatUsd(token.price)}
        </Text>
        {token.price != null && formatDualPrice(token.price).secondary !== '' && (
          <Text variant="caption" color="textMuted" numeric>
            {formatDualPrice(token.price).secondary}
          </Text>
        )}
      </View>

      {/* 24h change */}
      <View
        style={{
          minWidth: CHANGE_COL_WIDTH,
          alignItems: 'center',
          paddingHorizontal: spacing.xs,
          paddingVertical: 4,
          borderRadius: radius.sm,
          backgroundColor: hasChange ? changeBg : colors.surfaceAlt,
        }}
      >
        <Text
          variant="caption"
          weight="700"
          numeric
          style={{ color: hasChange ? changeColor : colors.textMuted }}
        >
          {hasChange ? `${isUp ? '+' : ''}${(token.change24h ?? 0).toFixed(2)}%` : '—'}
        </Text>
      </View>
    </PressableScale>
  );
};

export interface TokenMarketListProps {
  /** Already filtered and pinned for `tab` — see utils/spotTokens#tokensForTab. */
  tokens: TokenMarket[];
  tab: SpotTabKey;
  onTabChange: (tab: SpotTabKey) => void;
  favoriteSymbols: ReadonlySet<string>;
  onToggleFavorite: (symbol: string) => void;
  onTokenPress: (symbol: string) => void;
  /** Card heading. Omit on Spot Trade, where the pane is already titled. */
  title?: string;
  /** Cap the rows rendered (Home shows a preview). Unlimited when omitted. */
  limit?: number;
  /** Footer action shown only when `limit` actually hides rows. */
  onSeeAll?: () => void;
  /**
   * Listings fetch failure. Distinguishes "we couldn't load the catalogue"
   * from "this tab is empty", which otherwise look identical.
   */
  error?: string;
  onRetry?: () => void;
  /** Outer spacing is the caller's business — the card owns none. */
  style?: StyleProp<ViewStyle>;
}

export const TokenMarketList: React.FC<TokenMarketListProps> = ({
  tokens,
  tab,
  onTabChange,
  favoriteSymbols,
  onToggleFavorite,
  onTokenPress,
  title,
  limit,
  onSeeAll,
  error,
  onRetry,
  style,
}) => {
  const { colors, spacing } = useTheme();
  const visible = limit != null ? tokens.slice(0, limit) : tokens;
  const hasMore = limit != null && tokens.length > limit;
  const empty = TAB_EMPTY[tab] ?? {
    title: 'Nothing in this tab yet',
    subtitle: 'Tokens assigned to this category will show up here.',
  };

  return (
    <Surface elevation="sm" style={[{ overflow: 'hidden' }, style]}>
      {title && (
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md }}>
          <Text variant="bodySm" weight="700">
            {title}
          </Text>
        </View>
      )}

      {/* Category tabs (taxonomy + computed). flexGrow:0 keeps the row hugging
          its content height — without it the horizontal ScrollView fills the
          column and the chips stretch to giant lozenges. */}
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        style={{ flexGrow: 0, flexShrink: 0 }}
        contentContainerStyle={{
          alignItems: 'center',
          gap: spacing.xs,
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: spacing.md,
        }}
      >
        {SPOT_TABS.map((t) => (
          <Chip
            key={t.key}
            label={t.label}
            leftIcon={t.key === 'favorites' ? 'heart' : t.key === 'hot' ? 'trendingUp' : undefined}
            selected={tab === t.key}
            onPress={() => onTabChange(t.key)}
          />
        ))}
      </ScrollView>

      {/* Column header — mirrors the exchange convention the rows follow. The
          leading spacer and the gap match TokenRow's avatar so "Name" sits over
          the symbol and "24h chg%" over the change chip. */}
      <View
        style={{
          flexDirection: 'row',
          gap: spacing.md,
          paddingHorizontal: spacing.lg,
          paddingBottom: spacing.xs,
        }}
      >
        <View style={{ width: AVATAR_SIZE }} />
        <Text variant="caption" color="textMuted" style={{ flex: 1 }}>
          Name
        </Text>
        <Text variant="caption" color="textMuted">
          Last Price
        </Text>
        <Text variant="caption" color="textMuted" style={{ minWidth: CHANGE_COL_WIDTH, textAlign: 'center' }}>
          24h chg%
        </Text>
      </View>
      <Divider />

      {visible.length === 0 && error ? (
        <PressableScale
          onPress={onRetry}
          accessibilityRole={onRetry ? 'button' : undefined}
          style={{ paddingVertical: spacing.lg }}
        >
          <EmptyState
            icon="closeCircle"
            title="Couldn’t load markets"
            subtitle={onRetry ? `${error} — tap to retry` : error}
          />
        </PressableScale>
      ) : visible.length === 0 ? (
        <View style={{ paddingVertical: spacing.lg }}>
          <EmptyState icon="clock" title={empty.title} subtitle={empty.subtitle} />
        </View>
      ) : (
        visible.map((token, index) => (
          <View key={token.symbol}>
            <TokenRow
              token={token}
              isFavorite={favoriteSymbols.has(token.symbol)}
              onToggleFavorite={() => onToggleFavorite(token.symbol)}
              onPress={() => onTokenPress(token.symbol)}
            />
            {index < visible.length - 1 && <Divider inset={spacing.lg} />}
          </View>
        ))
      )}

      {hasMore && onSeeAll && (
        <>
          <Divider />
          <PressableScale
            onPress={onSeeAll}
            accessibilityRole="button"
            accessibilityLabel="View all tokens"
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.xs,
              paddingVertical: spacing.md,
            }}
          >
            <Text variant="bodySm" weight="700" style={{ color: colors.accent }}>
              View all
            </Text>
            <Icon name="chevronRight" size={14} color={colors.accent} />
          </PressableScale>
        </>
      )}
    </Surface>
  );
};

export default TokenMarketList;
