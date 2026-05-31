// app/p2p/index.tsx
//
// P2P marketplace — browse sell-ads (user buys crypto) and buy-ads (user sells
// crypto to a merchant). Restyled onto the TSA Signature design system.
import { router } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  distinctAdProviders,
  formatTokenAmount,
  type P2PAd,
} from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import CurrencySelector from '../../components/currency/CurrencySelector';
import PaymentMethodFilter from '../../components/p2p/PaymentMethodFilter';
import SellerTrustBadges from '../../components/p2p/SellerTrustBadges';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Badge,
  Button,
  Chip,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  SegmentedControl,
  SkeletonGroup,
  StatusPill,
  Text,
} from '@/components/ui';

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };
const TOKENS = ['USDT', 'USDC', 'MCGP'];

const P2PMarketScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [token, setToken] = useState<string>('USDT');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  const [adSide, setAdSide] = useState<'sell' | 'buy'>('sell');
  const { isMerchant, role, loading: roleLoading } = useUserRole();
  const isAdmin = !!role && ['admin', 'super_admin', 'superadmin'].includes(role);
  const { currency, setCurrency: setMarketCurrency } = useP2pCurrency();
  const [providerFilter, setProviderFilter] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(false);
    const res = await adsApi.list({ side: adSide, token, fiatCurrency: currency.code });
    if (res.success && res.data) {
      setAds(res.data);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [adSide, token, currency.code]);

  useEffect(() => {
    load();
  }, [load]);

  // Clear any stale provider filter when currency/token/side changes.
  useEffect(() => {
    setProviderFilter(null);
  }, [currency.code, token, adSide]);

  const visibleAds = providerFilter
    ? ads.filter((a) => (a.paymentMethodSummary ?? []).some((s) => s.provider === providerFilter))
    : ads;

  const createRoute = adSide === 'sell' ? '/p2p/create-ad' : '/p2p/create-buy-ad';
  const onCreate = () => router.push((isMerchant ? createRoute : '/merchants/merchant-request') as never);

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
        {isAdmin && !roleLoading && (
          <PressableScale onPress={() => router.push('/admin/p2p-disputes')} style={{ marginBottom: spacing.md }}>
            <AccentCard padding="md" railColor={colors.warning}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="shield" size={20} color={colors.warning} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Dispute console
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Review and resolve escrow disputes.
                  </Text>
                </View>
                <Icon name="chevronRight" size={18} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="title1" weight="800">
            {adSide === 'sell' ? `Buy ${token}` : `Sell ${token}`}
          </Text>
          <CurrencySelector variant="header" selectedCode={currency.code} onSelectCode={setMarketCurrency} />
        </View>

        <View style={{ marginTop: spacing.md }}>
          <SegmentedControl
            segments={[
              { key: 'sell', label: 'Buy' },
              { key: 'buy', label: 'Sell' },
            ]}
            value={adSide}
            onChange={(k) => {
              setAdSide(k as 'sell' | 'buy');
              setLoading(true);
            }}
          />
        </View>

        {/* Section nav (My ads / Orders / Banks / Pool) lives in the bottom footer
            now; the Market screen keeps only the create CTA. */}
        <Button
          title={adSide === 'sell' ? `Sell ${token}` : 'Post a buy ad'}
          leftIcon="plus"
          variant="secondary"
          onPress={onCreate}
          style={{ marginTop: spacing.lg }}
        />

        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.md, marginBottom: spacing.lg }}>
          {adSide === 'sell'
            ? `Buy ${token} with ${currency.code}. Funds are escrowed on chain until the seller releases.`
            : `Sell ${token} to a merchant for ${currency.code}. Deposit + escrow happen at trade open.`}
        </Text>

        {adSide === 'sell' && !roleLoading && !isMerchant && (
          <PressableScale onPress={() => router.push('/merchants/merchant-request')} style={{ marginBottom: spacing.lg }}>
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="storefront" size={24} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Want to sell?
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Become a merchant to publish P2P ads.
                  </Text>
                </View>
                <Icon name="chevronRight" size={20} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        <View style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.lg }}>
          {TOKENS.map((t) => (
            <Chip key={t} label={t} selected={token === t} onPress={() => setToken(t)} />
          ))}
        </View>

        {!loading && !error && ads.length > 0 && (
          <View style={{ marginBottom: spacing.lg }}>
            <PaymentMethodFilter options={distinctAdProviders(ads)} selected={providerFilter} onChange={setProviderFilter} />
          </View>
        )}

        {loading ? (
          <SkeletonGroup shape="adCard" count={4} />
        ) : error ? (
          <ErrorState subtitle="Couldn’t load the market. Check your connection." onRetry={() => { setLoading(true); load(); }} />
        ) : ads.length === 0 ? (
          <EmptyState
            icon="storefront"
            title={adSide === 'sell' ? `No ${token} sellers yet` : `No buyers for ${token} yet`}
            subtitle={`for ${currency.code}. Pull to refresh or try another currency.`}
            actionLabel={adSide === 'sell' ? 'Post an ad' : undefined}
            onAction={adSide === 'sell' ? onCreate : undefined}
          />
        ) : visibleAds.length === 0 ? (
          <EmptyState
            icon="filter"
            title="No ads match this filter"
            subtitle="Try a different payment method."
            actionLabel="Clear filter"
            onAction={() => setProviderFilter(null)}
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {visibleAds.map((ad) => (
              <AdCard key={ad.id} ad={ad} adSide={adSide} />
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const AdCard: React.FC<{ ad: P2PAd; adSide: 'sell' | 'buy' }> = ({ ad, adSide }) => {
  const { colors, spacing } = useTheme();
  const decimals = TOKEN_DECIMALS[ad.token] ?? 18;
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  const isFloat = ad.rateType === 'float';
  const isStale = !!ad.rateStale;
  const sellerLabel = ad.sellerUsername || `seller-${ad.sellerId.slice(0, 4)}`;

  return (
    <PressableScale
      onPress={() => {
        if (isStale) return;
        router.push(`/p2p/ad/${ad.id}`);
      }}
      disabled={isStale}
      style={{ opacity: isStale ? 0.6 : 1 }}
    >
      <AccentCard railColor={isStale ? colors.textMuted : colors.accentRail}>
        <View style={{ flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between' }}>
          <PressableScale
            onPress={() => router.push((`/p2p/seller/${ad.sellerId}`) as never)}
            style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}
          >
            <Avatar name={sellerLabel} size={36} />
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                @{sellerLabel}
              </Text>
              <Text variant="caption" color="textMuted">
                {adSide === 'sell' ? `Sells ${ad.token}` : `Buys ${ad.token}`}
              </Text>
            </View>
          </PressableScale>
          {isFloat && <Badge label="Float" icon="trendingUp" color={colors.accent} background={colors.accentMuted} size="sm" />}
        </View>

        <SellerTrustBadges stats={ad.sellerStats} />

        <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4, marginTop: spacing.sm }}>
          <AmountText value={ad.rate} currency={ad.fiatCurrency} variant="title1" />
          <Text variant="bodySm" color="textMuted">
            / {ad.token}
          </Text>
        </View>

        {isStale && <StatusPill kind="ad" status="rateStale" size="sm" style={{ marginTop: spacing.xs }} />}

        <Divider spacing="md" />

        <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
          <View>
            <Text variant="overline" color="textMuted">
              AVAILABLE
            </Text>
            <Text variant="bodySm" weight="700">
              {remaining} {ad.token}
            </Text>
          </View>
          {ad.minLimit && ad.maxLimit && (
            <View style={{ alignItems: 'flex-end' }}>
              <Text variant="overline" color="textMuted">
                LIMIT
              </Text>
              <Text variant="bodySm" weight="700" numeric>
                {`${currencyShort(ad.fiatCurrency, ad.minLimit)} – ${currencyShort(ad.fiatCurrency, ad.maxLimit)}`}
              </Text>
            </View>
          )}
        </View>

        {ad.paymentMethodSummary && ad.paymentMethodSummary.length > 0 && (
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs, marginTop: spacing.md }}>
            {ad.paymentMethodSummary.slice(0, 4).map((s) => (
              <Badge key={s.provider} label={s.label} size="sm" />
            ))}
          </View>
        )}
      </AccentCard>
    </PressableScale>
  );
};

// Compact fiat for tight limit rows (symbol + grouped integer).
function currencyShort(currency: string, amount: string | number): string {
  const SYM: Record<string, string> = { NGN: '₦', USD: '$', GHS: '₵', KES: 'KSh', ZAR: 'R', GBP: '£', EUR: '€', CAD: 'C$' };
  const n = typeof amount === 'string' ? Number(amount) : amount;
  const grouped = isFinite(n) ? Math.round(n).toLocaleString('en-US') : '0';
  const sym = SYM[currency?.toUpperCase()] ?? '';
  return sym ? `${sym}${grouped}` : `${grouped} ${currency}`;
}

export default P2PMarketScreen;
