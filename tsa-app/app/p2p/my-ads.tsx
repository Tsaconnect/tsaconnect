// app/p2p/my-ads.tsx
//
// Seller's own ads — list with status filter, pause/resume, and on-chain close.
// Close flows through the prepare → sign → broadcast → confirm pattern just
// like create-ad and trade actions. Restyled onto the TSA Signature design
// system; behavior + API calls are unchanged.
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  formatTokenAmount,
  p2pChainKey,
  p2pTokenDecimals,
  unsignedTxToTxLike,
  type P2PAd,
} from '../../services/p2p';
import { broadcastTransaction, getActiveWallet, signTransaction } from '../../services/wallet';
import { useUserRole } from '../../hooks/useUserRole';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Button,
  Chip,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  SkeletonGroup,
  StatusPill,
  Text,
  useToast,
} from '@/components/ui';

type StatusFilter = 'all' | P2PAd['status'];

const STATUS_TABS: { key: StatusFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'active', label: 'Active' },
  { key: 'paused', label: 'Paused' },
  { key: 'pending', label: 'Pending' },
  { key: 'exhausted', label: 'Done' },
  { key: 'cancelled', label: 'Cancelled' },
];

const MyAdsScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [filter, setFilter] = useState<StatusFilter>('all');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [acting, setActing] = useState<string>(''); // adId currently undergoing an action
  const { isMerchant, loading: roleLoading } = useUserRole();

  const load = useCallback(async () => {
    const res = await adsApi.listMine(filter === 'all' ? {} : { status: filter });
    if (res.success && res.data) {
      setAds(res.data);
      setLoadError(null);
    } else {
      // Keep the existing list visible (don't clobber on a transient fail)
      // but surface a retry banner so the user knows their view is stale.
      setLoadError(res.message || 'Could not refresh ads');
    }
    setLoading(false);
    setRefreshing(false);
  }, [filter]);

  // useFocusEffect only fires on focus events, so a filter-pill swap while
  // already focused wouldn't re-fetch. Pair it with a plain useEffect on
  // `load` so any dependency change (filter) refreshes the list.
  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));
  useEffect(() => {
    setLoading(true);
    load();
  }, [load]);

  const onTogglePause = async (ad: P2PAd) => {
    if (acting) return;
    setActing(ad.id);
    const res = ad.status === 'paused' ? await adsApi.resume(ad.id) : await adsApi.pause(ad.id);
    if (!res.success) {
      toast.error(res.message || 'Server rejected the request');
    } else {
      toast.success(ad.status === 'paused' ? 'Ad resumed' : 'Ad paused');
    }
    setActing('');
    load();
  };

  const onCancel = (ad: P2PAd) => {
    if (acting) return;
    // Hold the action lock for the duration of the confirm dialog so a
    // concurrent pause/resume on the same card can't sneak in and starve
    // the cancel flow with a silent `if (acting) return` no-op.
    setActing(ad.id);
    Alert.alert(
      'Cancel this ad?',
      'Cancelling revokes your voucher on-chain. The locked portion stays in your pool ' +
        'balance and any trades already in progress will continue until they finish.',
      [
        { text: 'Keep ad', style: 'cancel', onPress: () => setActing('') },
        { text: 'Cancel ad', style: 'destructive', onPress: () => doCancel(ad) },
      ],
      { onDismiss: () => setActing((cur) => (cur === ad.id ? '' : cur)) },
    );
  };

  const doCancel = async (ad: P2PAd) => {
    let broadcastHash: string | null = null;
    try {
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) throw new Error('No wallet found');
      const prep = await adsApi.prepareCancel(ad.id, sellerAddress);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');
      // @ts-expect-error APP-12 handles the null unsignedTx of a buy-ad cancel
      const signed = await signTransaction(unsignedTxToTxLike(prep.data.unsignedTx));
      const resp = await broadcastTransaction(p2pChainKey(ad.chainId), signed);
      broadcastHash = resp.hash;
      const conf = await adsApi.confirmCancel(ad.id, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
      toast.success('Ad cancelled');
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        Alert.alert(
          'Cancel submitted, server confirm failed',
          `Tx ${broadcastHash} was broadcast on chain but the server couldn't record it ` +
            `(${detail}). Wait a minute for the reconciler to catch up; contact support if not.`,
        );
      } else {
        toast.error(detail);
      }
    } finally {
      setActing('');
      load();
    }
  };

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
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="title1" weight="700">
            My Ads
          </Text>
          <Button
            title={isMerchant ? 'New ad' : 'Become merchant'}
            leftIcon="plus"
            size="sm"
            fullWidth={false}
            onPress={() =>
              router.push(isMerchant ? '/p2p/create-ad' : '/merchants/merchant-request')
            }
          />
        </View>

        {/* Entry point to the escrow-funds (reclaim/withdraw) screen — the
            standalone Pool tab is gone (spec: hide-the-pool, Architecture 3),
            so My Ads is where a seller reaches their on-chain escrow balance.
            Passive indicator is kept bounded: rather than firing N balance
            reads (this list only has per-ad remaining/reserved, not the
            seller's pool available/locked), we show a static subtitle that
            explains the available/locked semantics and invites a tap to
            manage. */}
        {!roleLoading && isMerchant && (
          <PressableScale
            onPress={() => router.push('/p2p/pool')}
            style={{ marginTop: spacing.lg }}
          >
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="wallet" size={24} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Escrow funds
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Available is withdrawable; locked is reserved against open trades — tap to
                    manage.
                  </Text>
                </View>
                <Icon name="chevronRight" size={20} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        {/* Mirrors the marketplace-side banner: non-merchants reading their
            (empty) My Ads list get a one-tap path to onboarding instead of
            hitting a 403 inside the create-ad flow. */}
        {!roleLoading && !isMerchant && (
          <PressableScale
            onPress={() => router.push('/merchants/merchant-request')}
            style={{ marginTop: spacing.lg }}
          >
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="storefront" size={24} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Become a merchant
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Required to publish P2P ads. Apply in a minute.
                  </Text>
                </View>
                <Icon name="chevronRight" size={20} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={{ marginTop: spacing.lg, marginBottom: spacing.lg }}
          contentContainerStyle={{ gap: spacing.sm, paddingHorizontal: 2 }}
        >
          {STATUS_TABS.map((t) => (
            <Chip
              key={t.key}
              label={t.label}
              selected={filter === t.key}
              onPress={() => setFilter(t.key)}
            />
          ))}
        </ScrollView>

        {loadError && !loading && (
          <View style={{ marginBottom: spacing.lg }}>
            <ErrorState
              subtitle={loadError}
              onRetry={() => {
                setLoading(true);
                load();
              }}
            />
          </View>
        )}

        {loading ? (
          <SkeletonGroup shape="adCard" count={4} />
        ) : ads.length === 0 ? (
          <EmptyState
            icon="storefront"
            title={filter === 'all' ? 'No ads yet' : `No ${filter} ads`}
            subtitle={
              filter === 'all'
                ? "You haven't posted any ads yet. Tap 'New ad' to start selling."
                : `No ads in '${filter}' status.`
            }
            actionLabel={isMerchant ? 'New ad' : undefined}
            onAction={isMerchant ? () => router.push('/p2p/create-ad') : undefined}
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {ads.map((ad) => (
              <AdCard
                key={ad.id}
                ad={ad}
                busy={acting === ad.id}
                onTogglePause={onTogglePause}
                onCancel={onCancel}
              />
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const AdCard: React.FC<{
  ad: P2PAd;
  busy: boolean;
  onTogglePause: (ad: P2PAd) => void;
  onCancel: (ad: P2PAd) => void;
}> = ({ ad, busy, onTogglePause, onCancel }) => {
  const { spacing } = useTheme();
  const decimals = p2pTokenDecimals(ad.chainId, ad.token);
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  const reserved = formatTokenAmount(ad.reserved, decimals, 2);
  const total = formatTokenAmount(ad.amount, decimals, 2);
  const canPauseResume = ad.status === 'active' || ad.status === 'paused';
  const canCancel = ad.status === 'active' || ad.status === 'paused' || ad.status === 'pending';
  const hasRate = !!ad.rate && ad.rate !== '0';

  return (
    <PressableScale onPress={() => router.push(`/p2p/ad/${ad.id}`)}>
      <AccentCard>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <View style={{ flex: 1 }}>
            <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
              {hasRate ? (
                <AmountText value={ad.rate} currency={ad.fiatCurrency} variant="title2" />
              ) : (
                <Text variant="title2" weight="700">
                  —
                </Text>
              )}
              <Text variant="bodySm" color="textMuted">
                / {ad.token}
              </Text>
            </View>
            {ad.rateType === 'float' && (
              <Text variant="caption" color={ad.rateStale ? 'warning' : 'textMuted'}>
                {ad.rateStale ? 'Float · stale feed' : 'Float · auto'}
              </Text>
            )}
          </View>
          <StatusPill kind="ad" status={ad.status} size="sm" />
        </View>

        <Divider spacing="md" />

        <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
          <Text variant="bodySm" color="textSecondary">
            Available
          </Text>
          <Text variant="bodySm" weight="600" numeric>
            {remaining} {ad.token}
          </Text>
        </View>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
          <Text variant="bodySm" color="textSecondary">
            Reserved
          </Text>
          <Text variant="bodySm" weight="600" numeric>
            {reserved} {ad.token}
          </Text>
        </View>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
          <Text variant="bodySm" color="textSecondary">
            Total
          </Text>
          <Text variant="bodySm" weight="600" numeric>
            {total} {ad.token}
          </Text>
        </View>

        {(canPauseResume || canCancel) && (
          <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md }}>
            {canPauseResume && (
              <Button
                title={ad.status === 'paused' ? 'Resume' : 'Pause'}
                leftIcon={ad.status === 'paused' ? 'play' : 'pause'}
                variant="secondary"
                size="sm"
                fullWidth={false}
                disabled={busy}
                onPress={() => onTogglePause(ad)}
              />
            )}
            {canCancel && (
              <Button
                title="Cancel ad"
                leftIcon="close"
                variant="danger"
                size="sm"
                fullWidth={false}
                loading={busy}
                disabled={busy}
                onPress={() => onCancel(ad)}
              />
            )}
          </View>
        )}
      </AccentCard>
    </PressableScale>
  );
};

export default MyAdsScreen;
