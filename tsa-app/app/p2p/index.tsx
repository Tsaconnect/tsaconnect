// app/p2p/index.tsx
//
// P2P marketplace — browse sell-ads (user buys crypto) and buy-ads (user sells
// crypto to a merchant). Restyled onto the TSA Signature design system.
import { router, Stack, useFocusEffect, useLocalSearchParams } from 'expo-router';
import { goBack } from '@/utils/back';
import { SERVICES_TAB } from '@/constants/routes';
import React, { useCallback, useEffect, useState } from 'react';
import { BackHandler, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  chainsApi,
  distinctAdProviders,
  formatTokenAmount,
  p2pTokenDecimals,
  type P2PAd,
  type P2PChainInfo,
} from '../../services/p2p';
import { getChainByChainId } from '../../constants/chains';
import { useIsPaymentMerchant } from '../../hooks/useIsPaymentMerchant';
import { shouldShowPmCta, PM_MERCHANT_REQUEST_ROUTE } from '@/utils/pmCta';
import { lpCta } from '@/utils/lpCta';
import { RAILS, railLabel } from '../../constants/rails';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import CurrencySelector from '../../components/currency/CurrencySelector';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Badge,
  Button,
  Dropdown,
  EmptyState,
  ErrorState,
  Icon,
  Input,
  PressableScale,
  SegmentedControl,
  SkeletonGroup,
  StatusPill,
  Text,
} from '@/components/ui';

// Fallback token list — used when the /p2p/chains endpoint is unreachable so
// the marketplace still works in degraded mode. No MCGP: it is no longer a P2P
// asset, and the API hides MCGP ads from browse.
const FALLBACK_TOKENS = ['USDT', 'USDC'];

// Sonic chain IDs (mainnet + testnet) — used for the Sonic-first sort order.
const SONIC_CHAIN_IDS = new Set([146, 14601]);

function sortChainInfos(chains: P2PChainInfo[]): P2PChainInfo[] {
  return [...chains].sort((a, b) => {
    const aS = SONIC_CHAIN_IDS.has(a.chainId) ? 0 : 1;
    const bS = SONIC_CHAIN_IDS.has(b.chainId) ? 0 : 1;
    if (aS !== bS) return aS - bS;
    return a.chainId - b.chainId;
  });
}

const P2PMarketScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [token, setToken] = useState<string>('USDT');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  // Optional deep-link intent from the marketplace P2P heading: ?side=buy means
  // "I want to BUY crypto" → browse sell-ads (adSide 'sell'); ?side=sell means
  // "I want to SELL crypto" → browse buy-ads (adSide 'buy').
  // ?pm=1 → Payment Merchant mode (PM-filtered feed); absent/0 → normal P2P.
  const { side: sideParam, pm: pmParam } = useLocalSearchParams<{ side?: string; pm?: string }>();
  const isPmView = pmParam === '1';
  const [adSide, setAdSide] = useState<'sell' | 'buy'>(sideParam === 'sell' ? 'buy' : 'sell');
  const { isPm, kind, liquidityAvailable, loading: pmLoading } = useIsPaymentMerchant();
  const { currency, setCurrency: setMarketCurrency } = useP2pCurrency();
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
  // Minimum trade amount (fiat, in the selected currency). Shows ads whose
  // max limit can accommodate this amount and up.
  const [minAmount, setMinAmount] = useState<number | null>(null);

  // ── Chain / token data from the backend ─────────────────────────────────
  const [enabledChains, setEnabledChains] = useState<P2PChainInfo[]>([]);
  const [chainsFetched, setChainsFetched] = useState(false);

  // Derive the selectable token list from the union of assets; fall back to
  // FALLBACK_TOKENS if the endpoint hasn't responded yet or returned an error.
  const availableTokens: string[] = React.useMemo(() => {
    if (!chainsFetched || enabledChains.length === 0) return FALLBACK_TOKENS;
    const symbols = new Set(enabledChains.flatMap((c) => c.assets));
    return Array.from(symbols);
  }, [enabledChains, chainsFetched]);

  // Chain filter: null = "All chains" (omit chainId from list call).
  const [chainFilter, setChainFilter] = useState<number | null>(null);

  // The sorted chains that offer the selected token — used to populate the
  // chain filter dropdown.
  const chainsForToken: P2PChainInfo[] = React.useMemo(() => {
    if (enabledChains.length === 0) return [];
    return sortChainInfos(enabledChains.filter((c) => c.assets.includes(token)));
  }, [enabledChains, token]);

  // Fetch the enabled chains on mount; graceful fallback on failure.
  useEffect(() => {
    (async () => {
      try {
        const res = await chainsApi.list();
        if (res.success && res.data && res.data.length > 0) {
          setEnabledChains(res.data);
        }
      } catch (err) {
        console.warn('[p2p/index] chainsApi.list failed, using fallback tokens', err);
      } finally {
        setChainsFetched(true);
      }
    })();
  }, []);

  const load = useCallback(async () => {
    setError(false);
    const res = await adsApi.list({
      side: adSide,
      token,
      fiatCurrency: currency.code,
      ...(chainFilter != null ? { chainId: chainFilter } : {}),
      // PM mode: include only PM ads (pm_id set). Normal P2P: exclude PM ads.
      // This keeps PM ads out of the regular feed and the PM tab out of normal P2P.
      pm: isPmView ? true : false,
    });
    if (res.success && res.data) {
      setAds(res.data);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [adSide, token, currency.code, chainFilter, isPmView]);

  useEffect(() => {
    load();
  }, [load]);

  // The P2P marketplace is entered as a section takeover (the marketplace tab
  // redirects here via router.replace), so the root back-stack can be empty on
  // this screen — without this, Android's hardware back would exit the app.
  // Detail/flow screens are pushed onto this stack and keep their normal back
  // behaviour, so the handler only runs while the index is focused.
  //
  // iOS has no hardware back, which is why this screen also needs the shared
  // header chevron from app/p2p/_layout.tsx: for a long time this handler was
  // the app's only way out of the P2P marketplace, and iOS users had none.
  useFocusEffect(
    useCallback(() => {
      const onBackPress = () => {
        goBack(SERVICES_TAB);
        return true;
      };
      const sub = BackHandler.addEventListener('hardwareBackPress', onBackPress);
      return () => sub.remove();
    }, []),
  );

  // Clear stale filters when currency/token/side changes (the amount filter is
  // denominated in the current currency, so it can't carry across switches).
  // Also reset chainFilter when token changes — the new token may not be on
  // the previously-selected chain.
  useEffect(() => {
    setProviderFilter(null);
    setMinAmount(null);
    setChainFilter(null);
  }, [currency.code, token, adSide]);

  const filtersActive = providerFilter != null || minAmount != null;
  const visibleAds = ads.filter((a) => {
    if (providerFilter && !(a.paymentMethodSummary ?? []).some((s) => s.provider === providerFilter)) {
      return false;
    }
    if (minAmount != null) {
      const max = a.maxLimit != null ? Number(a.maxLimit) : null;
      // Ads with no max limit are unbounded → always accommodate the amount.
      if (max != null && isFinite(max) && max < minAmount) return false;
    }
    return true;
  });

  const createRoute = adSide === 'sell' ? '/p2p/create-ad' : '/p2p/create-buy-ad';
  const onCreate = () => router.push(createRoute as never);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* One screen, two rails — the header names whichever feed is showing. */}
      <Stack.Screen options={{ title: isPmView ? railLabel('pm') : railLabel('p2p') }} />
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
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <View style={{ flex: 1 }}>
            <Text variant="title1" weight="700">
              {isPmView ? railLabel('pm') : (adSide === 'sell' ? `Buy ${token}` : `Sell ${token}`)}
            </Text>
            <Text variant="caption" color="textSecondary">
              {isPmView ? RAILS.pm.tagline : RAILS.p2p.tagline}
            </Text>
          </View>
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
          style={{ marginTop: spacing.md }}
        />

        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm, marginBottom: spacing.md }}>
          {adSide === 'sell'
            ? `Buy ${token} with ${currency.code}. Funds are escrowed on chain until the seller releases.`
            : `Sell ${token} to a merchant for ${currency.code}. Deposit + escrow happen at trade open.`}
        </Text>

        {shouldShowPmCta({ isPmView, isPm: isPm || kind === 'lp', loading: pmLoading }) && (
          <PressableScale onPress={() => router.push(PM_MERCHANT_REQUEST_ROUTE as never)} style={{ marginBottom: spacing.lg }}>
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="storefront" size={24} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Become a Payment Merchant
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Apply to offer instant buy & sell with NGN settlement.
                  </Text>
                </View>
                <Icon name="chevronRight" size={20} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        {(() => {
          const cta = lpCta({ isPm, kind, liquidityAvailable, loading: pmLoading });
          return cta ? (
            <PressableScale onPress={() => router.push(cta.route as never)} style={{ marginBottom: spacing.lg }}>
              <AccentCard padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <Icon name="wallet" size={24} color={colors.accent} />
                  <View style={{ flex: 1 }}>
                    <Text variant="bodyMed" weight="700">{cta.title}</Text>
                    <Text variant="caption" color="textSecondary">{cta.subtitle}</Text>
                  </View>
                  <Icon name="chevronRight" size={20} color={colors.textMuted} />
                </View>
              </AccentCard>
            </PressableScale>
          ) : null;
        })()}

        {/* Filter row — Token always; Chain (when >1 chain offers the token);
            Amount + Payment once ads load. One horizontally-scrollable line. */}
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
          style={{ marginBottom: spacing.md }}
          contentContainerStyle={{ alignItems: 'center', gap: spacing.sm, paddingRight: spacing.lg }}
        >
          <TokenFilter
            token={token}
            tokens={availableTokens}
            onChange={(t) => {
              setToken(t);
              setLoading(true);
            }}
          />
          {chainsForToken.length > 1 && (
            <ChainFilter
              chains={chainsForToken}
              selected={chainFilter}
              onChange={(id) => {
                setChainFilter(id);
                setLoading(true);
              }}
            />
          )}
          {!loading && !error && ads.length > 0 && (
            <>
              <AmountFilter
                value={minAmount}
                onChange={setMinAmount}
                currencyCode={currency.code}
                currencySymbol={currency.symbol}
              />
              <PaymentFilter
                options={distinctAdProviders(ads)}
                selected={providerFilter}
                onChange={setProviderFilter}
              />
            </>
          )}
        </ScrollView>

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
            title="No ads match these filters"
            subtitle={
              minAmount != null
                ? `No ads handle ${currencyShort(currency.code, minAmount)}+ with this payment method.`
                : 'Try a different payment method.'
            }
            actionLabel={filtersActive ? 'Clear filters' : undefined}
            onAction={
              filtersActive
                ? () => {
                    setProviderFilter(null);
                    setMinAmount(null);
                  }
                : undefined
            }
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
  const decimals = p2pTokenDecimals(ad.chainId, ad.token);
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  const isFloat = ad.rateType === 'float';
  const isStale = !!ad.rateStale;
  const sellerLabel = ad.sellerUsername || `seller-${ad.sellerId.slice(0, 4)}`;

  // Fold the seller's trust signals into a single caption line (Bybit-style)
  // instead of a full-height badge row — keeps each card to ~1/3 screen.
  const stats = ad.sellerStats;
  const isNew = !stats || stats.isNew;
  const statsText = (() => {
    if (isNew || !stats) return '';
    const parts = [`${stats.totalOrders} orders`];
    if (stats.completionRate != null) parts.push(`${(stats.completionRate * 100).toFixed(0)}%`);
    const rel = relTime(stats.avgReleaseSecs);
    if (rel) parts.push(`releases ${rel}`);
    return parts.join(' · ');
  })();

  // One tight meta line: limit range + remaining, instead of a divider + 2-col row.
  const limitText =
    ad.minLimit && ad.maxLimit
      ? `${currencyShort(ad.fiatCurrency, ad.minLimit)}–${currencyShort(ad.fiatCurrency, ad.maxLimit)}`
      : null;
  const metaText = [limitText ? `Limit ${limitText}` : null, `${remaining} ${ad.token} avail`]
    .filter(Boolean)
    .join('  ·  ');

  return (
    <PressableScale
      onPress={() => {
        if (isStale) return;
        router.push(`/p2p/ad/${ad.id}`);
      }}
      disabled={isStale}
      style={{ opacity: isStale ? 0.6 : 1 }}
    >
      <AccentCard padding="md" railColor={isStale ? colors.textMuted : colors.accentRail}>
        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          {/* LEFT — seller, rate, limits and payment, tightly stacked */}
          <View style={{ flex: 1 }}>
            <PressableScale
              onPress={() => router.push((`/p2p/seller/${ad.sellerId}`) as never)}
              style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}
            >
              <Avatar name={sellerLabel} size={32} />
              <View style={{ flex: 1 }}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <Text variant="bodySm" weight="700" numberOfLines={1} style={{ flexShrink: 1 }}>
                    @{sellerLabel}
                  </Text>
                  {stats?.online && (
                    <View style={{ width: 7, height: 7, borderRadius: 4, backgroundColor: colors.online }} />
                  )}
                  {isNew ? (
                    <Badge label="New" size="sm" color={colors.info} background={colors.infoBg} />
                  ) : stats?.trusted ? (
                    <Badge label="Trusted" icon="verified" size="sm" color={colors.accent} background={colors.accentMuted} />
                  ) : stats?.verified ? (
                    <Badge label="Verified" icon="checkCircle" size="sm" color={colors.success} background={colors.successBg} />
                  ) : null}
                  {isFloat && (
                    <Badge label="Float" icon="trendingUp" size="sm" color={colors.accent} background={colors.accentMuted} />
                  )}
                </View>
                {statsText ? (
                  <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 1 }}>
                    {statsText}
                  </Text>
                ) : (
                  <Text variant="caption" color="textMuted" numberOfLines={1}>
                    {adSide === 'sell' ? `Sells ${ad.token}` : `Buys ${ad.token}`}
                  </Text>
                )}
              </View>
            </PressableScale>

            <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4, marginTop: spacing.sm }}>
              <AmountText value={ad.rate} currency={ad.fiatCurrency} variant="title2" />
              <Text variant="caption" color="textMuted">
                / {ad.token}
              </Text>
            </View>

            <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 2 }}>
              {metaText}
            </Text>

            {(ad.paymentMethodSummary && ad.paymentMethodSummary.length > 0 || ad.chainId) && (
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs, marginTop: spacing.sm }}>
                {ad.paymentMethodSummary && ad.paymentMethodSummary.slice(0, 3).map((s) => (
                  <Badge key={s.provider} label={s.label} size="sm" />
                ))}
                {ad.chainId && (() => {
                  const chainLabel = getChainByChainId(ad.chainId)?.shortName;
                  return chainLabel ? (
                    <Badge
                      key="chain"
                      label={chainLabel}
                      size="sm"
                      color={colors.accent}
                      background={colors.accentMuted}
                    />
                  ) : null;
                })()}
              </View>
            )}

            {isStale && <StatusPill kind="ad" status="rateStale" size="sm" style={{ marginTop: spacing.xs }} />}
          </View>

          {/* RIGHT — primary CTA filling the otherwise-empty column, centered
              against the info stack so it adds no extra height. */}
          {!isStale && (
            <Button
              title={adSide === 'sell' ? 'Buy' : 'Sell'}
              size="sm"
              fullWidth={false}
              onPress={() => router.push(`/p2p/ad/${ad.id}`)}
              style={{ alignSelf: 'center', minWidth: 76 }}
            />
          )}
        </View>
      </AccentCard>
    </PressableScale>
  );
};

// Shared filter-row chip trigger (Token / Amount / Payment). `active` paints
// the accent treatment; the value text doubles as the label so each chip always
// shows what it filters.
const FilterChip: React.FC<{ label: string; active?: boolean }> = ({ label, active }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.xs,
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.sm,
        borderRadius: radius.sm,
        borderWidth: 1,
        borderColor: active ? colors.accent : colors.border,
        backgroundColor: active ? colors.accentMuted : colors.surface,
      }}
    >
      <Text variant="bodySm" weight="600" color={active ? 'accent' : 'textSecondary'} numberOfLines={1}>
        {label}
      </Text>
      <Icon name="chevronDown" size={16} color={active ? colors.accent : colors.textMuted} />
    </View>
  );
};

// Single-select option list for the compact dropdown menus (Token / Payment).
const OptionList: React.FC<{
  options: { key: string; label: string }[];
  selectedKey: string;
  onPick: (key: string) => void;
}> = ({ options, selectedKey, onPick }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ paddingVertical: spacing.xs }}>
      {options.map((o) => {
        const sel = o.key === selectedKey;
        return (
          <PressableScale
            key={o.key}
            onPress={() => onPick(o.key)}
            accessibilityRole="button"
            accessibilityState={{ selected: sel }}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              paddingHorizontal: spacing.md,
              paddingVertical: spacing.sm,
              backgroundColor: sel ? colors.accentMuted : 'transparent',
            }}
          >
            <Text variant="bodySm" weight="600" numberOfLines={1}>
              {o.label}
            </Text>
            {sel && <Icon name="check" size={16} color={colors.accent} />}
          </PressableScale>
        );
      })}
    </View>
  );
};

// Token (asset) selector. Uses the dynamically-fetched token list, falling
// back to FALLBACK_TOKENS when the endpoint hasn't responded yet.
const TokenFilter: React.FC<{
  token: string;
  tokens: string[];
  onChange: (t: string) => void;
}> = ({ token, tokens, onChange }) => (
  <Dropdown align="start" menuWidth={180} renderTrigger={() => <FilterChip label={token} active />}>
    {({ close }) => (
      <OptionList
        options={tokens.map((t) => ({ key: t, label: t }))}
        selectedKey={token}
        onPick={(t) => {
          if (t !== token) onChange(t);
          close();
        }}
      />
    )}
  </Dropdown>
);

// Chain filter — shown only when more than one chain offers the selected token.
// null selection means "All chains".
const ALL_CHAINS = '__all__';
const ChainFilter: React.FC<{
  chains: P2PChainInfo[];
  selected: number | null;
  onChange: (chainId: number | null) => void;
}> = ({ chains, selected, onChange }) => {
  const selectedLabel =
    selected == null
      ? 'All chains'
      : getChainByChainId(selected)?.shortName ?? String(selected);
  return (
    <Dropdown
      align="start"
      menuWidth={200}
      renderTrigger={() => <FilterChip label={selectedLabel} active={selected != null} />}
    >
      {({ close }) => (
        <OptionList
          options={[
            { key: ALL_CHAINS, label: 'All chains' },
            ...chains.map((c) => ({
              key: String(c.chainId),
              label: getChainByChainId(c.chainId)?.shortName ?? String(c.chainId),
            })),
          ]}
          selectedKey={selected == null ? ALL_CHAINS : String(selected)}
          onPick={(key) => {
            onChange(key === ALL_CHAINS ? null : Number(key));
            close();
          }}
        />
      )}
    </Dropdown>
  );
};

// Payment-method filter (themed; replaces the legacy bottom-sheet component).
// `null` selection means "All Payment Methods".
const ALL_PAYMENTS = '__all__';
const PaymentFilter: React.FC<{
  options: { provider: string; label: string }[];
  selected: string | null;
  onChange: (provider: string | null) => void;
}> = ({ options, selected, onChange }) => {
  const selectedLabel =
    selected == null ? 'Payment' : options.find((o) => o.provider === selected)?.label ?? 'Payment';
  return (
    <Dropdown align="start" menuWidth={240} renderTrigger={() => <FilterChip label={selectedLabel} active={selected != null} />}>
      {({ close }) => (
        <OptionList
          options={[
            { key: ALL_PAYMENTS, label: 'All Payment Methods' },
            ...options.map((o) => ({ key: o.provider, label: o.label })),
          ]}
          selectedKey={selected ?? ALL_PAYMENTS}
          onPick={(key) => {
            onChange(key === ALL_PAYMENTS ? null : key);
            close();
          }}
        />
      )}
    </Dropdown>
  );
};

// Minimum-amount filter — a chip that opens a numeric field; filters the list
// to ads whose max limit can accommodate the entered amount and up (visibleAds).
const AmountFilter: React.FC<{
  value: number | null;
  onChange: (v: number | null) => void;
  currencyCode: string;
  currencySymbol: string;
}> = ({ value, onChange, currencyCode, currencySymbol }) => (
  <Dropdown
    align="start"
    menuWidth={240}
    renderTrigger={() => (
      <FilterChip label={value != null ? `≥ ${currencyShort(currencyCode, value)}` : 'Amount'} active={value != null} />
    )}
  >
    {({ close }) => (
      <AmountMenu
        initial={value}
        currencyCode={currencyCode}
        currencySymbol={currencySymbol}
        onApply={(v) => {
          onChange(v);
          close();
        }}
        onClear={() => {
          onChange(null);
          close();
        }}
      />
    )}
  </Dropdown>
);

const AmountMenu: React.FC<{
  initial: number | null;
  currencyCode: string;
  currencySymbol: string;
  onApply: (v: number | null) => void;
  onClear: () => void;
}> = ({ initial, currencyCode, currencySymbol, onApply, onClear }) => {
  const { spacing } = useTheme();
  const [draft, setDraft] = useState(initial != null ? String(initial) : '');
  const apply = () => {
    const n = parseFloat(draft);
    onApply(isFinite(n) && n > 0 ? n : null);
  };
  return (
    <View style={{ padding: spacing.md, gap: spacing.sm }}>
      <Text variant="overline" color="textMuted">
        MIN AMOUNT ({currencyCode})
      </Text>
      <Input
        value={draft}
        onChangeText={(t) => setDraft(t.replace(/[^0-9.]/g, ''))}
        keyboardType="numeric"
        placeholder="0"
        returnKeyType="done"
        onSubmitEditing={apply}
        leftAdornment={
          <Text variant="bodyMed" color="textMuted">
            {currencySymbol}
          </Text>
        }
      />
      <View style={{ flexDirection: 'row', gap: spacing.sm }}>
        <Button title="Clear" variant="secondary" size="sm" onPress={onClear} style={{ flex: 1 }} />
        <Button title="Apply" size="sm" onPress={apply} style={{ flex: 1 }} />
      </View>
    </View>
  );
};

// Approx. avg-release time for the seller-stats caption (~Ns / ~Nm).
function relTime(secs?: number): string | null {
  if (secs == null) return null;
  if (secs < 90) return `~${secs}s`;
  return `~${Math.round(secs / 60)}m`;
}

// Compact fiat for tight limit rows (symbol + grouped integer).
function currencyShort(currency: string, amount: string | number): string {
  const SYM: Record<string, string> = { NGN: '₦', USD: '$', GHS: '₵', KES: 'KSh', ZAR: 'R', GBP: '£', EUR: '€', CAD: 'C$' };
  const n = typeof amount === 'string' ? Number(amount) : amount;
  const grouped = isFinite(n) ? Math.round(n).toLocaleString('en-US') : '0';
  const sym = SYM[currency?.toUpperCase()] ?? '';
  return sym ? `${sym}${grouped}` : `${grouped} ${currency}`;
}

export default P2PMarketScreen;
