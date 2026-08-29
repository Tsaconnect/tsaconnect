// app/wallet/it/intent/[id].tsx
//
// Instant Transfer order detail — status progress for a single buy/sell intent.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ScrollView, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams, router } from 'expo-router';
import { useFocusEffect, useIsFocused } from '@react-navigation/native';
import { ethers } from 'ethers';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { Button, Card, Divider, Icon, Spinner, SummaryCard, SummaryRow, Text } from '@/components/ui';
import { getITIntent, reportStableDeposit, IT_STABLE_DEPOSIT_CLAIMED, type ITIntent } from '@/services/itApi';
import { formatTokenAmount } from '@/services/p2p';
import { isTerminalStableStatus, statusToHops } from '@/utils/stableSellQuote';

const fmtMcgp = (wei?: string) => {
  try {
    return Number(ethers.formatUnits(wei || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 });
  } catch {
    return '0';
  }
};
const fmtNaira = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};
const fmtRate = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};

// AssetAmount is base units at 6 decimals for both USDC and USDT
// (tsa-api-go internal/models/it_models.go:81-84) — fmtMcgp (18-decimal wei)
// is simply the wrong function for this asset, not merely an unused one.
const fmtAsset = (baseUnits?: string) => formatTokenAmount(baseUnits || '0', 6, 6);

// Matches sell-stable.tsx's own `pendingTxKey` (not exported from that
// screen) — Task 7 writes a broadcast hash here before reporting it, keyed by
// intent so this screen can find it by id alone.
const pendingTxKey = (intentId: string) => `it_stable_pending_tx_${intentId}`;

const STABLE_ASSETS = new Set(['USDC', 'USDT']);

function statusLabel(status: string): string {
  switch (status) {
    case 'pending_payment':
      return 'Awaiting transfer';
    case 'processing':
      return 'Processing';
    case 'credited':
      return 'Complete';
    case 'debited':
      return 'Payout in progress';
    case 'settled':
      return 'Complete';
    case 'awaiting_rate':
      return 'Awaiting rate';
    case 'failed':
      return 'Failed';
    case 'cancelled':
      return 'Cancelled';
    case 'reversed':
      return 'Reversed';
    // Stablecoin sell statuses (it_models.go:42-45) — falling through to
    // `default` below would render the raw snake_case status to the user.
    case 'pending_deposit':
      return 'Awaiting your deposit';
    case 'deposit_reported':
      return 'Confirming your deposit';
    case 'deposit_confirmed':
      return 'Deposit confirmed';
    case 'converting':
      return 'Converting to cash';
    default:
      return status || '—';
  }
}

function shortDate(iso?: string): string {
  if (!iso) return '—';
  try {
    return new Date(iso).toLocaleString(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short',
    });
  } catch {
    return iso;
  }
}

export default function ITIntentDetail() {
  const { colors, spacing, radius } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [intent, setIntent] = useState<ITIntent | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // Whether an unreported deposit hash is sitting in AsyncStorage for this
  // intent, and whether that check has completed. Both start "unknown" so the
  // resume UI (Step 4) never flashes before the check has actually run.
  const [pendingHash, setPendingHash] = useState<string | null>(null);
  const [hashChecked, setHashChecked] = useState(false);
  const [addressCopied, setAddressCopied] = useState(false);
  // Guards the deposit-report retry to ONE attempt per screen open (see the
  // effect below) — not a ref to `intent` itself, which changes identity on
  // every poll tick.
  const retriedForId = useRef<string | null>(null);

  const load = useCallback(async () => {
    if (!id) {
      setError('Missing order id');
      setLoading(false);
      return;
    }
    setError('');
    const r = await getITIntent(id);
    if (r.success && r.data?.intent) setIntent(r.data.intent);
    else setError(r.message || 'Order not found');
    setLoading(false);
  }, [id]);

  const isStableAsset = intent ? STABLE_ASSETS.has(intent.asset ?? '') : false;

  // MCGP path: unchanged behavior — fetch once per focus, no continuous poll.
  // This also covers the very first load for a stablecoin intent too, since
  // isStableAsset is false until `intent` has loaded at least once.
  useFocusEffect(
    useCallback(() => {
      if (!isStableAsset) load();
    }, [load, isStableAsset])
  );

  // Stablecoin path: poll while focused and non-terminal, so hops advance
  // without a manual pull-to-refresh. Mirrors the P2P trade cockpit
  // (app/p2p/trade/[tradeId].tsx:181-221).
  //
  // Gating on isStableAsset (not just isTerminalStableStatus) matters:
  // 'credited' is MCGP buy's own terminal status and is NOT in
  // isTerminalStableStatus's set ({settled, reversed, failed, cancelled}) —
  // applying this poll to every asset would leave a completed MCGP buy
  // polling forever in the background, the exact bug this gate exists to
  // avoid.
  const isFocused = useIsFocused();
  const pollingActive = isStableAsset && isFocused && !isTerminalStableStatus(intent?.status ?? '');

  useEffect(() => {
    if (!pollingActive) return;
    load();
    const t = setInterval(load, 10_000);
    return () => clearInterval(t);
  }, [load, pollingActive]);

  // Retry an unreported deposit, ONCE per screen open — not inside the poll
  // above, which would re-POST the report every 10s until the key cleared.
  //
  // Task 7 persists the broadcast hash to AsyncStorage before reporting it,
  // so a report that failed then (dead network, app killed) is recoverable
  // here. Gated on NON-TERMINAL rather than `pending_deposit`: the case this
  // exists for — the report succeeded server-side but the response was lost —
  // leaves the intent at `deposit_reported`, so a pending_deposit-only guard
  // would never fire and the key would accumulate forever, once per
  // abandoned intent.
  //
  // Safe to re-report: the same hash on an already-reported intent returns
  // early server-side (ReportStableDeposit's own idempotency, not the
  // Idempotency-Key middleware — `req` mints a fresh key per call, so the
  // middleware sees an unrelated request each time). Clear the key on success
  // OR on already-claimed, since neither will ever succeed later.
  //
  // `retriedForId` — not the effect's dependency array — is what limits this
  // to one attempt: `intent` is a NEW object every poll tick (see above), so
  // an effect that only re-ran when `id` changed would either fire before
  // `intent` ever loaded (and never again), or, keyed on `intent`, would
  // re-fire every tick without this guard.
  useEffect(() => {
    if (!id || !intent || retriedForId.current === id) return;
    retriedForId.current = id;
    (async () => {
      const key = pendingTxKey(id);
      let pending: string | null = null;
      try {
        pending = await AsyncStorage.getItem(key);
      } catch {
        // Unreadable — the resume UI (Step 4) just won't have anything to
        // retry; it can't tell "nothing was sent" from "couldn't check."
      }
      setPendingHash(pending);
      setHashChecked(true);
      if (pending && !isTerminalStableStatus(intent.status)) {
        const r = await reportStableDeposit(id, pending);
        if (r.success || r.code === IT_STABLE_DEPOSIT_CLAIMED) {
          try {
            await AsyncStorage.removeItem(key);
          } catch {
            // Next screen open retries the removal too — harmless.
          }
          setPendingHash(null);
        }
      }
    })();
  }, [id, intent]);

  const handleCopyDepositAddress = useCallback(async () => {
    if (!intent?.depositAddress) return;
    await Clipboard.setStringAsync(intent.depositAddress);
    setAddressCopied(true);
    setTimeout(() => setAddressCopied(false), 2000);
  }, [intent?.depositAddress]);

  if (loading && !intent) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner />
      </SafeAreaView>
    );
  }

  if (error && !intent) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.danger, textAlign: 'center' }}>{error}</Text>
      </SafeAreaView>
    );
  }

  if (!intent) return null;

  const isBuy = intent.direction === 'buy';
  const title = isBuy ? 'Buy' : 'Sell';

  // Resume applies only when nothing was ever sent: status is still
  // pending_deposit AND the AsyncStorage check above (not merely "hasn't run
  // yet") found no stored hash.
  const showResume = isStableAsset && hashChecked && !pendingHash && intent.status === 'pending_deposit';
  // Only a Polygon-source intent can be resumed with a plain "send to this
  // address" — depositAddress is the LiFi bridge's *destination*, and a
  // direct same-chain send only bypasses the bridge when the source chain
  // already IS Polygon. Sending from another chain straight to this address
  // (same 0x string is valid on any EVM chain) would land on the WRONG
  // chain — never reaching Flutterwave's Polygon side, and never verifiable
  // by ConfirmStableDeposit. sell-stable.tsx encodes the same distinction as
  // `reviewBridged = reviewChainKey !== 'polygon'`.
  const canResumeDirect = showResume && intent.sourceChain?.toUpperCase() === 'POLYGON' && !!intent.depositAddress;

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        <Card>
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
            {title}
          </Text>
          <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
            {statusLabel(intent.status)}
          </Text>
          <SummaryCard>
            <SummaryRow label="Status" value={statusLabel(intent.status)} />
            <Divider spacing="xs" />
            {isStableAsset ? (
              <>
                <SummaryRow label="You sell" value={`${fmtAsset(intent.assetAmount)} ${intent.asset}`} />
                <Divider spacing="xs" />
                <SummaryRow label="You receive" value={`₦${fmtNaira(intent.ngnKobo)}`} />
              </>
            ) : isBuy ? (
              <>
                <SummaryRow label="You pay" value={`₦${fmtNaira(intent.ngnKobo)}`} />
                <Divider spacing="xs" />
                <SummaryRow label="You get" value={`${fmtMcgp(intent.mcgpAmount)} MCGP`} />
              </>
            ) : (
              <>
                <SummaryRow label="You sell" value={`${fmtMcgp(intent.mcgpAmount)} MCGP`} />
                <Divider spacing="xs" />
                <SummaryRow label="You receive" value={`₦${fmtNaira(intent.ngnKobo)}`} />
              </>
            )}
            <Divider spacing="xs" />
            <SummaryRow
              label="Rate"
              value={isStableAsset ? `₦${fmtRate(intent.rateKobo)} / ${intent.asset}` : `₦${fmtRate(intent.rateKobo)} / MCGP`}
            />
            <Divider spacing="xs" />
            <SummaryRow label="Created" value={shortDate(intent.createdAt)} />
            {intent.txHash ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Tx" value={`${intent.txHash.slice(0, 10)}…${intent.txHash.slice(-6)}`} />
              </>
            ) : null}
          </SummaryCard>
          {isStableAsset ? (
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.sm }}>
                Progress
              </Text>
              <View style={{ gap: spacing.sm }}>
                {statusToHops(intent.status).map((hop) => (
                  <View key={hop.key} style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                    {hop.state === 'done' ? (
                      <Icon name="checkCircle" size={20} color={colors.success} />
                    ) : hop.state === 'failed' ? (
                      <Icon name="closeCircle" size={20} color={colors.danger} />
                    ) : hop.state === 'active' ? (
                      <Spinner size="small" />
                    ) : (
                      <View
                        style={{
                          width: 18,
                          height: 18,
                          borderRadius: 9,
                          borderWidth: 2,
                          borderColor: colors.border,
                        }}
                      />
                    )}
                    <Text
                      variant="bodySm"
                      style={{ color: hop.state === 'pending' ? colors.textMuted : colors.textPrimary }}
                    >
                      {hop.label}
                    </Text>
                  </View>
                ))}
              </View>
            </View>
          ) : null}
          {intent.failureReason ? (
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                Note
              </Text>
              <Text variant="bodySm" style={{ color: colors.textPrimary }}>
                {intent.failureReason}
              </Text>
            </View>
          ) : null}
          {intent.status === 'pending_payment' ? (
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.md }}>
              Transfer any Naira amount into your Instant Transfer account. This order updates when
              payment is confirmed at the live rate.
            </Text>
          ) : null}
        </Card>

        {showResume ? (
          canResumeDirect ? (
            <Card>
              <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
                Nothing sent yet
              </Text>
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
                Send exactly {fmtAsset(intent.assetAmount)} {intent.asset} on Polygon to this address
                to continue this order.
              </Text>
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                  padding: spacing.sm,
                  borderRadius: radius.sm,
                  borderWidth: 1,
                  borderColor: colors.border,
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="bodySm" style={{ color: colors.textPrimary, flex: 1 }} numberOfLines={1}>
                  {intent.depositAddress}
                </Text>
                <Icon name="copy" size={18} color={colors.textMuted} />
              </View>
              <Button
                title={addressCopied ? 'Address copied' : `Continue — send your ${intent.asset}`}
                leftIcon={addressCopied ? 'checkCircle' : 'copy'}
                onPress={handleCopyDepositAddress}
              />
            </Card>
          ) : (
            <Card>
              <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
                Nothing sent yet
              </Text>
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
                This order was never completed — it's a harmless, unused record. Start a new sell to
                continue.
              </Text>
              <Button
                title="Start again"
                variant="secondary"
                onPress={() => router.push(`/wallet/it/sell-stable?asset=${intent.asset}`)}
              />
            </Card>
          )
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}
