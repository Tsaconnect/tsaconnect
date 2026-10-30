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
  p2pTokenDecimals,
  type P2PAd,
} from '../../services/p2p';
import { p2pErrorMessage } from '../../services/p2pErrors';
import { runP2PTx, type P2PFollowUp } from '../../services/p2pTx';
import { adCancelFollowUp, adCancelThrownFollowUp } from '../../utils/p2pAdCancel';
import { applyFollowUp } from '../../utils/p2pFollowUp';
import { getActiveWallet } from '../../services/wallet';
import { useKeyedMessages } from '@/hooks/useKeyedMessages';
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
  InlineMessage,
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
  // Per ad, so a refused pause/cancel stays under the card it belongs to until the next attempt.
  const [adErrors, setAdError] = useKeyedMessages();
  // Ads whose cancel was broadcast but not recorded yet: shown as sent, not
  // offered for cancel again until a later reload still shows them open.
  const [cancelSent, setCancelSent] = useState<ReadonlySet<string>>(new Set());

  const load = useCallback(async () => {
    const res = await adsApi.listMine(filter === 'all' ? {} : { status: filter });
    if (res.success && res.data) {
      setAds(res.data);
      setLoadError(null);
      setCancelSent(new Set());
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
    setAdError(ad.id);
    const res = ad.status === 'paused' ? await adsApi.resume(ad.id) : await adsApi.pause(ad.id);
    if (!res.success) {
      setAdError(ad.id, p2pErrorMessage(res, 'Server rejected the request'));
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
      'Cancelling closes this ad. The locked portion stays in your pool ' +
        'balance and any trades already in progress will continue until they finish.',
      [
        { text: 'Keep ad', style: 'cancel', onPress: () => setActing('') },
        { text: 'Cancel ad', style: 'destructive', onPress: () => doCancel(ad) },
      ],
      { onDismiss: () => setActing((cur) => (cur === ad.id ? '' : cur)) },
    );
  };

  const doCancel = async (ad: P2PAd) => {
    let next: P2PFollowUp = { reload: true };
    setAdError(ad.id);
    try {
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) throw new Error('No wallet found');
      const r = await runP2PTx({
        chainId: ad.chainId,
        walletAddress: sellerAddress,
        action: 'cancel_ad',
        adId: ad.id,
        // A buy-ad cancel is DB-only (unsignedTx:null), so there is nothing to sponsor.
        skipGas: ad.side === 'buy',
        prepare: () => adsApi.prepareCancel(ad.id, sellerAddress),
        confirm: (h) => adsApi.confirmCancel(ad.id, h),
      });
      next = adCancelFollowUp(r);
      if (next.cancelSent) setCancelSent((prev) => new Set(prev).add(ad.id));
    } catch (err) {
      next = adCancelThrownFollowUp(err);
    } finally {
      setActing('');
      applyFollowUp(next, { toast, reload: load, onError: (m) => setAdError(ad.id, m) });
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
            title="New ad"
            leftIcon="plus"
            size="sm"
            fullWidth={false}
            onPress={() => router.push('/p2p/create-ad')}
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
            actionLabel="New ad"
            onAction={() => router.push('/p2p/create-ad')}
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {ads.map((ad) => (
              <View key={ad.id} style={{ gap: spacing.sm }}>
                <AdCard
                  ad={ad}
                  busy={acting === ad.id}
                  cancelSent={cancelSent.has(ad.id)}
                  onTogglePause={onTogglePause}
                  onCancel={onCancel}
                />
                <InlineMessage message={adErrors[ad.id]} onDismiss={() => setAdError(ad.id)} />
              </View>
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
  cancelSent: boolean;
  onTogglePause: (ad: P2PAd) => void;
  onCancel: (ad: P2PAd) => void;
}> = ({ ad, busy, cancelSent, onTogglePause, onCancel }) => {
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
            {canCancel && cancelSent && (
              <Text variant="caption" color="textMuted" style={{ alignSelf: 'center' }}>
                Cancel sent — updating once recorded
              </Text>
            )}
            {canCancel && !cancelSent && (
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
