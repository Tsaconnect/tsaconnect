// app/p2p/index.tsx
//
// P2P marketplace — browse sell-ads (user buys crypto) and buy-ads (user
// sells crypto to a merchant). Toggle between the two sides via a Buy/Sell
// segmented control.
import { router } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  distinctAdProviders,
  formatFiat,
  formatTokenAmount,
  type P2PAd,
} from '../../services/p2p';
import { P2P_GOLD } from '../../constants/p2p';
import { useUserRole } from '../../hooks/useUserRole';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import CurrencySelector from '../../components/currency/CurrencySelector';
import PaymentMethodFilter from '../../components/p2p/PaymentMethodFilter';
import SellerTrustBadges from '../../components/p2p/SellerTrustBadges';

const GOLD = P2P_GOLD;

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

const P2PMarketScreen: React.FC = () => {
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [token, setToken] = useState<string>('USDT');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  // adSide: 'sell' = browsing sell-ads (user buys crypto);
  //         'buy'  = browsing buy-ads (user sells crypto to a merchant).
  const [adSide, setAdSide] = useState<'sell' | 'buy'>('sell');
  // Backend gates ad creation behind merchant role (p2p_handler.go: CreateAd
  // returns 403 for non-merchants). Surface that gate in the UI so a regular
  // user gets a clear "become a merchant" prompt instead of filling the form
  // only to hit a 403 at publish time.
  const { isMerchant, loading: roleLoading } = useUserRole();
  const { currency, setCurrency: setMarketCurrency } = useP2pCurrency();
  const [providerFilter, setProviderFilter] = useState<string | null>(null);

  const load = useCallback(async () => {
    const res = await adsApi.list({ side: adSide, token, fiatCurrency: currency.code });
    if (res.success && res.data) {
      setAds(res.data);
    }
    setLoading(false);
    setRefreshing(false);
  }, [adSide, token, currency.code]);

  useEffect(() => {
    load();
  }, [load]);

  // Clear any stale provider filter when the market currency, token, or side
  // changes, so it can't hide every ad after switching.
  useEffect(() => {
    setProviderFilter(null);
  }, [currency.code, token, adSide]);

  const visibleAds = providerFilter
    ? ads.filter((a) =>
        (a.paymentMethodSummary ?? []).some((s) => s.provider === providerFilter),
      )
    : ads;

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        contentContainerStyle={styles.scroll}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <View style={styles.headerRow}>
          <Text style={styles.heading}>
            {adSide === 'sell' ? `Buy ${token}` : `Sell ${token}`}
          </Text>
          <CurrencySelector
            variant="header"
            selectedCode={currency.code}
            onSelectCode={setMarketCurrency}
          />
        </View>

        {/* Buy / Sell tab toggle */}
        <View style={styles.sideToggle}>
          <TouchableOpacity
            style={[styles.sideTab, adSide === 'sell' && styles.sideTabActive]}
            onPress={() => { setAdSide('sell'); setLoading(true); }}
            activeOpacity={0.8}
          >
            <Text style={[styles.sideTabText, adSide === 'sell' && styles.sideTabTextActive]}>
              Buy
            </Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={[styles.sideTab, adSide === 'buy' && styles.sideTabActive]}
            onPress={() => { setAdSide('buy'); setLoading(true); }}
            activeOpacity={0.8}
          >
            <Text style={[styles.sideTabText, adSide === 'buy' && styles.sideTabTextActive]}>
              Sell
            </Text>
          </TouchableOpacity>
        </View>

        <View style={styles.actions}>
          {adSide === 'sell' ? (
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() =>
              router.push(isMerchant ? '/p2p/create-ad' : '/merchants/merchant-request')
            }
            activeOpacity={0.8}
          >
            <Icon name="add-circle" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>Sell</Text>
          </TouchableOpacity>
          ) : (
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() =>
              router.push(isMerchant ? ('/p2p/create-buy-ad' as any) : '/merchants/merchant-request')
            }
            activeOpacity={0.8}
          >
            <Icon name="add-circle" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>Post</Text>
          </TouchableOpacity>
          )}
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() => router.push('/p2p/my-ads')}
            activeOpacity={0.8}
          >
            <Icon name="list-alt" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>My ads</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() => router.push('/p2p/my-trades')}
            activeOpacity={0.8}
          >
            <Icon name="swap-horiz" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>My trades</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() => router.push('/p2p/payment-methods')}
            activeOpacity={0.8}
          >
            <Icon name="account-balance" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>Banks</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={styles.iconBtn}
            onPress={() => router.push('/p2p/pool')}
            activeOpacity={0.8}
          >
            <Icon name="savings" size={22} color={GOLD.dark} />
            <Text style={styles.iconBtnText}>Pool</Text>
          </TouchableOpacity>
        </View>

        <Text style={styles.subheading}>
          {adSide === 'sell'
            ? `Buy ${token} with ${currency.code}. Funds are escrowed on chain until the seller releases.`
            : `Sell ${token} to a merchant for ${currency.code}. Deposit + escrow happen at trade open.`}
        </Text>

        {/* "Want to sell?" banner — only shown on the sell-ad tab (buy crypto
            flow). On the buy-ad tab (user sells crypto) anyone can respond to
            an ad, so the merchant-only CTA doesn't apply. */}
        {adSide === 'sell' && !roleLoading && !isMerchant && (
          <TouchableOpacity
            style={styles.merchantBanner}
            onPress={() => router.push('/merchants/merchant-request')}
            activeOpacity={0.85}
          >
            <Icon name="storefront" size={24} color={GOLD.dark} />
            <View style={styles.merchantBannerText}>
              <Text style={styles.merchantBannerTitle}>Want to sell?</Text>
              <Text style={styles.merchantBannerDesc}>
                Become a merchant to publish P2P ads.
              </Text>
            </View>
            <Icon name="chevron-right" size={22} color={GOLD.dark} />
          </TouchableOpacity>
        )}

        <View style={styles.tokenRow}>
          {['USDT', 'USDC', 'MCGP'].map((t) => (
            <TouchableOpacity
              key={t}
              style={[styles.tokenChip, token === t && styles.tokenChipActive]}
              onPress={() => setToken(t)}
              activeOpacity={0.8}
            >
              <Text
                style={[styles.tokenChipText, token === t && styles.tokenChipTextActive]}
              >
                {t}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        {!loading && ads.length > 0 && (
          <View style={styles.filterRow}>
            <PaymentMethodFilter
              options={distinctAdProviders(ads)}
              selected={providerFilter}
              onChange={setProviderFilter}
            />
          </View>
        )}

        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : ads.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="storefront" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              {adSide === 'sell'
                ? `No ${token} sellers for ${currency.code} yet. Pull to refresh, try another currency, or post an ad.`
                : `No merchants buying ${token} for ${currency.code} yet. Pull to refresh or try another currency.`}
            </Text>
            {adSide === 'sell' && (
            <TouchableOpacity
              style={styles.postAdsBtn}
              onPress={() =>
                router.push(isMerchant ? '/p2p/create-ad' : '/merchants/merchant-request')
              }
              activeOpacity={0.85}
            >
              <Icon name="add-circle" size={18} color="#000" />
              <Text style={styles.postAdsBtnText}>Post Ads</Text>
            </TouchableOpacity>
            )}
          </View>
        ) : visibleAds.length === 0 ? (
          <View style={styles.noMatch}>
            <Text style={styles.noMatchText}>
              No ads match this payment method.
            </Text>
            <TouchableOpacity onPress={() => setProviderFilter(null)} activeOpacity={0.7}>
              <Text style={styles.clearFilterLink}>Clear filter</Text>
            </TouchableOpacity>
          </View>
        ) : (
          visibleAds.map((ad) => <AdCard key={ad.id} ad={ad} symbol={currency.symbol} adSide={adSide} />)
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const AdCard: React.FC<{ ad: P2PAd; symbol: string; adSide: 'sell' | 'buy' }> = ({ ad, symbol, adSide }) => {
  const decimals = TOKEN_DECIMALS[ad.token] ?? 18;
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  const isFloat = ad.rateType === 'float';
  const isStale = !!ad.rateStale;
  // sellerUsername is decorated server-side; fall back to a short ID for
  // legacy ads that pre-date the decoration.
  const sellerLabel = ad.sellerUsername || `seller-${ad.sellerId.slice(0, 4)}`;
  const sellerInitial = sellerLabel.charAt(0).toUpperCase();
  const minLimit = ad.minLimit ? formatFiat(ad.minLimit, 0) : null;
  const maxLimit = ad.maxLimit ? formatFiat(ad.maxLimit, 0) : null;
  return (
    <TouchableOpacity
      style={[styles.card, isStale && styles.cardDisabled]}
      onPress={() => {
        if (isStale) return;
        router.push(`/p2p/ad/${ad.id}`);
      }}
      activeOpacity={isStale ? 1 : 0.85}
      disabled={isStale}
    >
      <View style={styles.cardHeader}>
        <View style={styles.sellerBlock}>
          <TouchableOpacity
            style={styles.sellerAvatarNameRow}
            onPress={() => router.push(('/p2p/seller/' + ad.sellerId) as any)}
            activeOpacity={0.7}
          >
            <View style={styles.sellerAvatar}>
              <Text style={styles.sellerAvatarText}>{sellerInitial}</Text>
            </View>
            <View style={styles.sellerText}>
              <Text style={styles.sellerName} numberOfLines={1}>
                @{sellerLabel}
              </Text>
              <Text style={styles.sellerSub} numberOfLines={1}>
                {adSide === 'sell' ? `Sells ${ad.token}` : `Buys ${ad.token}`}
              </Text>
            </View>
          </TouchableOpacity>
          <SellerTrustBadges stats={ad.sellerStats} />
        </View>
        {isFloat ? (
          <View style={styles.floatChip}>
            <Icon name="trending-up" size={11} color={GOLD.dark} />
            <Text style={styles.floatChipText}>Float</Text>
          </View>
        ) : null}
      </View>

      <View style={styles.priceBlock}>
        <Text style={styles.priceCurrency}>{ad.fiatCurrency}</Text>
        <Text style={styles.priceValue}>{formatFiat(ad.rate)}</Text>
        <Text style={styles.priceUnit}>/ {ad.token}</Text>
      </View>

      {isStale && (
        <View style={styles.staleBadge}>
          <Icon name="error-outline" size={13} color="#DC3545" />
          <Text style={styles.staleBadgeText}>Live rate unavailable</Text>
        </View>
      )}

      <View style={styles.divider} />

      <View style={styles.metaGrid}>
        <View style={styles.metaCol}>
          <Text style={styles.metaLabel}>Available</Text>
          <Text style={styles.metaValue}>
            {remaining} {ad.token}
          </Text>
        </View>
        {minLimit && maxLimit && (
          <View style={[styles.metaCol, styles.metaColRight]}>
            <Text style={styles.metaLabel}>Limit</Text>
            <Text style={styles.metaValue}>
              {symbol}{minLimit} – {symbol}{maxLimit}
            </Text>
          </View>
        )}
      </View>

      {ad.paymentMethodSummary && ad.paymentMethodSummary.length > 0 && (
        <View style={styles.pmChips}>
          {ad.paymentMethodSummary.slice(0, 4).map((s) => (
            <View key={s.provider} style={styles.pmChip}>
              <Text style={styles.pmChipText}>{s.label}</Text>
            </View>
          ))}
        </View>
      )}
    </TouchableOpacity>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  headerRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  heading: { fontSize: 24, fontWeight: '800', color: '#000' },
  sideToggle: {
    flexDirection: 'row',
    backgroundColor: '#F0EAD6',
    borderRadius: 10,
    padding: 3,
    marginTop: 14,
  },
  sideTab: {
    flex: 1,
    paddingVertical: 8,
    borderRadius: 8,
    alignItems: 'center',
  },
  sideTabActive: { backgroundColor: GOLD.primary },
  sideTabText: { fontSize: 13, fontWeight: '700', color: GOLD.dark },
  sideTabTextActive: { color: '#000' },
  actions: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginTop: 16,
  },
  iconBtn: { alignItems: 'center' },
  iconBtnText: { fontSize: 11, color: GOLD.dark, marginTop: 2, fontWeight: '600' },
  subheading: { fontSize: 14, color: '#666', marginTop: 6, marginBottom: 16, lineHeight: 20 },
  merchantBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    backgroundColor: GOLD.light,
    borderWidth: 1,
    borderColor: GOLD.muted,
    borderRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: 12,
    marginBottom: 16,
  },
  merchantBannerText: { flex: 1 },
  merchantBannerTitle: { fontSize: 14, fontWeight: '800', color: '#000' },
  merchantBannerDesc: { fontSize: 12, color: '#666', marginTop: 2 },
  tokenRow: { flexDirection: 'row', gap: 10, marginBottom: 18 },
  tokenChip: {
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 8,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  tokenChipActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  tokenChipText: { fontWeight: '700', color: GOLD.dark, fontSize: 13 },
  tokenChipTextActive: { color: '#000' },
  filterRow: { flexDirection: 'row', marginBottom: 16 },
  empty: { alignItems: 'center', paddingVertical: 60 },
  emptyText: {
    marginTop: 12,
    fontSize: 14,
    color: '#888',
    textAlign: 'center',
    maxWidth: 260,
  },
  postAdsBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    marginTop: 20,
    paddingHorizontal: 20,
    paddingVertical: 11,
    borderRadius: 10,
    backgroundColor: GOLD.primary,
    borderWidth: 1,
    borderColor: GOLD.dark,
  },
  postAdsBtnText: { fontSize: 14, fontWeight: '800', color: '#000' },
  noMatch: { alignItems: 'center', paddingVertical: 48 },
  noMatchText: { fontSize: 14, color: '#888', textAlign: 'center' },
  clearFilterLink: {
    marginTop: 8,
    fontSize: 14,
    fontWeight: '700',
    color: GOLD.dark,
  },
  card: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#F0EAD6',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 6,
    elevation: 2,
  },
  cardDisabled: { opacity: 0.55 },
  cardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: 14,
  },
  sellerBlock: { flexDirection: 'column', flex: 1 },
  sellerAvatarNameRow: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  sellerAvatar: {
    width: 36,
    height: 36,
    borderRadius: 18,
    backgroundColor: GOLD.light,
    borderWidth: 1,
    borderColor: GOLD.muted,
    alignItems: 'center',
    justifyContent: 'center',
  },
  sellerAvatarText: { fontSize: 15, fontWeight: '800', color: GOLD.dark },
  sellerText: { flex: 1 },
  sellerName: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  sellerSub: { fontSize: 11, color: '#888', marginTop: 1, fontWeight: '500' },
  floatChip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
    backgroundColor: GOLD.light,
    paddingHorizontal: 7,
    paddingVertical: 3,
    borderRadius: 5,
  },
  floatChipText: { fontSize: 10, color: GOLD.dark, fontWeight: '700' },
  priceBlock: { flexDirection: 'row', alignItems: 'baseline', gap: 4 },
  priceCurrency: { fontSize: 12, fontWeight: '700', color: GOLD.dark, letterSpacing: 0.5 },
  priceValue: { fontSize: 26, fontWeight: '800', color: '#000', letterSpacing: -0.3 },
  priceUnit: { fontSize: 13, color: '#777', fontWeight: '600', marginLeft: 2 },
  staleBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginTop: 8,
    backgroundColor: '#FDECEF',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 6,
    alignSelf: 'flex-start',
  },
  staleBadgeText: { fontSize: 11, color: '#DC3545', fontWeight: '700' },
  divider: {
    height: 1,
    backgroundColor: '#F1ECDE',
    marginTop: 14,
    marginBottom: 12,
  },
  metaGrid: { flexDirection: 'row', justifyContent: 'space-between' },
  metaCol: { flex: 1 },
  metaColRight: { alignItems: 'flex-end' },
  metaLabel: { fontSize: 11, color: '#999', fontWeight: '600', textTransform: 'uppercase', letterSpacing: 0.4, marginBottom: 3 },
  metaValue: { fontSize: 13, color: '#1A1A1A', fontWeight: '700' },
  pmChips: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 6,
    marginTop: 12,
  },
  pmChip: {
    backgroundColor: GOLD.light,
    borderWidth: 1,
    borderColor: GOLD.muted,
    borderRadius: 6,
    paddingHorizontal: 8,
    paddingVertical: 4,
  },
  pmChipText: { fontSize: 11, color: GOLD.dark, fontWeight: '600' },
});

export default P2PMarketScreen;
