// app/wallet/it/intent/[id].tsx
//
// Instant Transfer order detail — status progress for a single buy/sell intent.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ScrollView, RefreshControl, View, Platform, Alert, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams, router } from 'expo-router';
import { useFocusEffect, useIsFocused } from '@react-navigation/native';
import { ethers } from 'ethers';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { Button, Card, Divider, Icon, Input, PressableScale, Spinner, Stepper, SummaryCard, SummaryRow, Text } from '@/components/ui';
import {
  getITIntent,
  reportStableDeposit,
  cancelITBuy,
  checkITBuyPayment,
  cancelStableBuy,
  IT_STABLE_DEPOSIT_CLAIMED,
  type ITIntent,
  type ITConversion,
  type ITPayTo,
} from '@/services/itApi';
import { PayIntoCard } from '@/components/it/PayIntoCard';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import { PayerDeclarationSheet } from '@/components/it/PayerDeclarationSheet';
import { HonourNotice } from '@/components/it/HonourNotice';
import { FeeLines } from '@/components/ramp/FeeLines';
import { ConversionProgress } from '@/components/it/ConversionProgress';
import { formatEstimate, formatPromisedAmount, isTerminal } from '@/services/paycrestApi';
import { getChainDisplay, type ChainKey } from '@/constants/chains';
import { honourOf } from '@/utils/rampHonour';
import { deliveryExplorerUrl, intentDisplayRateMinor, intentFeeLines } from '@/utils/itIntentView';
import { buyFeeBreakdown } from '@/utils/buyQuote';
import { cancelBuyErrorMessage, confirmCancelITBuy } from '@/utils/itBuyOrder';
import { formatTokenAmount } from '@/services/p2p';
import { formatKobo, isLikelyTxHash, isTerminalStableStatus, statusToHops } from '@/utils/stableSellQuote';
import { DisputeSection } from '@/components/disputes/DisputeSection';
// Buy equivalents of the sell mappings above — a stablecoin BUY intent has its
// own status set (payment_received, awaiting_funds, delivering,
// buy_awaiting_rate, …) that does not exist on the sell side, so statusToHops
// / isTerminalStableStatus are simply the wrong functions for it, not merely
// unused ones.
import { buyStatusToHops, buyStatusLabel, isAwaitingPayment, isBuyEstimate, isTerminalBuyStatus } from '@/utils/stableBuyQuote';

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
    // A pool buy whose slices are still being locked; its poll moves it on.
    case 'opening':
      return 'Setting up your order';
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

// A tappable row: children laid out across, centred.
function PressableRow({ onPress, children, style }: { onPress: () => void; children: React.ReactNode; style?: object }) {
  return (
    <PressableScale onPress={onPress} style={[{ flexDirection: 'row', alignItems: 'center', gap: 12 }, style]}>
      {children}
    </PressableScale>
  );
}

export default function ITIntentDetail() {
  const { colors, spacing, radius } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [intent, setIntent] = useState<ITIntent | null>(null);
  // Where to pay, for a buy still waiting for the transfer. The API sends it
  // only in that state, so its absence is not an error.
  const [payTo, setPayTo] = useState<ITPayTo | null>(null);
  const [conversion, setConversion] = useState<ITConversion | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // Whether an unreported deposit hash is sitting in AsyncStorage for this
  // intent, and whether that check has completed. Both start "unknown" so the
  // resume UI (Step 4) never flashes before the check has actually run.
  const [pendingHash, setPendingHash] = useState<string | null>(null);
  const [hashChecked, setHashChecked] = useState(false);
  const [addressCopied, setAddressCopied] = useState(false);
  // Cancel-while-unpaid, for a stablecoin buy reached from history — the same
  // escape hatch buy-stable.tsx's own resume view offers before this screen
  // ever exists for the intent.
  const [cancellingBuy, setCancellingBuy] = useState(false);
  const [cancelBuyError, setCancelBuyError] = useState('');
  // "I've sent the transfer": a one-off lookup ahead of the scheduled poll.
  const [checkingPayment, setCheckingPayment] = useState(false);
  const [paymentCheckNote, setPaymentCheckNote] = useState('');
  // "Paid from another account?" — the buyer names the account that paid.
  const [payerSheetOpen, setPayerSheetOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [amountCopied, setAmountCopied] = useState(false);
  // Manual recovery: a hash the user pastes in themselves. A bridged sell has
  // no other route back — sell-stable.tsx's history lookup only sees Polygon,
  // and the app told the user to report the transfer against this order.
  const [manualHash, setManualHash] = useState('');
  const [manualHashError, setManualHashError] = useState<string | null>(null);
  const [manualHashDone, setManualHashDone] = useState(false);
  const [reportingManual, setReportingManual] = useState(false);
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
    if (r.success && r.data?.intent) {
      setIntent(r.data.intent);
      setPayTo(r.data.payTo ?? null);
      setConversion(r.data.conversion ?? null);
    } else setError(r.message || 'Order not found');
    setLoading(false);
  }, [id]);

  const isStableAsset = intent ? STABLE_ASSETS.has(intent.asset ?? '') : false;

  // MCGP path: fetch once per focus. This also covers the very first load for a
  // stablecoin intent too, since isStableAsset is false until `intent` has
  // loaded at least once. An MCGP buy still awaiting the transfer additionally
  // polls (below).
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
  //
  // Which terminal SET applies depends on direction: a stablecoin buy's
  // terminal set is {settled, cancelled, failed} (isTerminalBuyStatus) and
  // deliberately excludes buy_awaiting_rate — money landed but could not be
  // priced, and an operator can reprice it back to payment_received. Using
  // the sell mapping's set here would treat buy_awaiting_rate as non-terminal
  // too (it isn't in that set either) by coincidence, but would also be
  // wrong on any future status the two mappings diverge on, so route on
  // direction explicitly rather than relying on that coincidence.
  //
  // An MCGP buy polls only while it is pending_payment: the user is on this
  // screen to pay, and it should flip to processing/credited on its own once
  // the transfer is matched. Every later MCGP status is either terminal or an
  // operator's to move, so the poll stops there — never on 'credited'.
  const isFocused = useIsFocused();
  const isStableBuy = isStableAsset && intent?.direction === 'buy';
  const isMcgpBuyAwaitingTransfer =
    !!intent && !isStableAsset && intent.direction === 'buy' && intent.status === 'pending_payment';
  const pollingActive =
    isFocused &&
    (isStableAsset
      ? !(isStableBuy
          ? isTerminalBuyStatus(intent?.status ?? '') && (!conversion || isTerminal(conversion.status))
          : isTerminalStableStatus(intent?.status ?? ''))
      : isMcgpBuyAwaitingTransfer);

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

  // …and "screen open" means FOCUS, not mount. Without this the guard survives
  // navigating away and back, so a retry that failed on a dead network is only
  // re-attempted after the screen is torn down entirely. Its own effect with
  // empty deps: hanging the reset off the MCGP focus effect above would fire
  // its cleanup whenever `isStableAsset` flips on first load.
  useFocusEffect(
    useCallback(
      () => () => {
        retriedForId.current = null;
      },
      []
    )
  );

  // Report a hand-entered hash against this intent, then reload so the status
  // and hops move. ReportStableDeposit is idempotent per (intent, hash), so a
  // hash the backend already knows returns early rather than double-counting;
  // an already-claimed one is a support case and says so.
  const submitManualHash = useCallback(async () => {
    if (!id || reportingManual) return;
    const hash = manualHash.trim();
    if (!isLikelyTxHash(hash)) {
      setManualHashError('That does not look like a transaction hash (0x…, 66 characters).');
      return;
    }
    setManualHashError(null);
    setReportingManual(true);
    const r = await reportStableDeposit(id, hash);
    setReportingManual(false);
    if (r.success) {
      setManualHashDone(true);
      setManualHash('');
      load();
      return;
    }
    setManualHashError(
      r.code === IT_STABLE_DEPOSIT_CLAIMED
        ? 'Another order has already claimed this transaction. Contact support — do not send again.'
        : r.message || "We couldn't record that transaction. Try again."
    );
  }, [id, load, manualHash, reportingManual]);

  const handleCopyDepositAddress = useCallback(async () => {
    if (!intent?.depositAddress) return;
    await Clipboard.setStringAsync(intent.depositAddress);
    setAddressCopied(true);
    setTimeout(() => setAddressCopied(false), 2000);
  }, [intent?.depositAddress]);

  // One cancel for both buy legs. The stablecoin leg keeps its original route
  // so this build still works against an API that predates /it/buy/:id/cancel.
  const cancelBuy = useCallback(async () => {
    if (!intent) return;
    setCancelBuyError('');
    setCancellingBuy(true);
    const r = isStableAsset ? await cancelStableBuy(intent.id) : await cancelITBuy(intent.id);
    setCancellingBuy(false);
    if (!r.success) {
      // it_buy_payment_arrived carries a message written for the user.
      setCancelBuyError(cancelBuyErrorMessage(r));
      // A refusal usually means the order moved on (a payment claimed it), so
      // show its real state instead of leaving the cancel card up.
      load();
      return;
    }
    load();
  }, [intent, isStableAsset, load]);

  const checkPayment = useCallback(async () => {
    if (!intent) return;
    setCheckingPayment(true);
    setPaymentCheckNote('');
    const r = await checkITBuyPayment(intent.id);
    if (r.success && r.data?.intent) {
      setIntent(r.data.intent);
      setPayTo(r.data.payTo ?? null);
      setConversion(r.data.conversion ?? null);
      if (r.data.intent.status === 'pending_payment') {
        setPaymentCheckNote(
          "We haven't seen your transfer yet. Bank transfers can take a few minutes to arrive — this page updates on its own as soon as it does."
        );
      }
    } else {
      // An API without this endpoint, or a network blip: the regular refresh
      // still tells the user where the order stands.
      await load();
    }
    setCheckingPayment(false);
  }, [intent, load]);

  // Cancelling after the transfer has been sent is the one unrecoverable
  // mistake on this screen. For a stablecoin buy, with no open buy intent left
  // the settlement path no longer matches the incoming charge and the naira is
  // auto-credited as a DIFFERENT asset; for an MCGP buy the money matches
  // nothing and is held and refunded. A one-tap destructive action with either
  // consequence gets a confirmation, not just body copy above it.
  const handleCancelBuy = useCallback(() => {
    if (!isStableAsset) {
      confirmCancelITBuy(() => {
        void cancelBuy();
      });
      return;
    }
    Alert.alert(
      'Cancel this purchase?',
      'If you have already sent the transfer, cancelling can route your money to a different asset. Only cancel if you have not paid.',
      [
        { text: 'Keep purchase', style: 'cancel' },
        { text: 'Cancel purchase', style: 'destructive', onPress: () => { void cancelBuy(); } },
      ]
    );
  }, [cancelBuy, isStableAsset]);

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

  // statusLabel above is scoped to MCGP (both directions) + stablecoin SELL.
  // A stablecoin BUY's own statuses (payment_received, awaiting_funds,
  // delivering, buy_awaiting_rate, …) aren't in it and would fall through to
  // the raw snake_case string, so route those through buyStatusLabel instead.
  const displayStatus = (status: string) => (isBuy && isStableAsset ? buyStatusLabel(status) : statusLabel(status));

  // buyStatusToHops's Hop (utils/stableBuyQuote.ts) carries no `key`, unlike
  // the sell mapping's — synthesize one from the index so both feed the same
  // render below without a union-typed `.key` access.
  const hops = !isStableAsset
    ? []
    : isBuy
      ? buyStatusToHops(intent.status).map((h, i) => ({ key: String(i), ...h }))
      : statusToHops(intent.status);

  const bridgedSource = !!intent.sourceChain && intent.sourceChain.toUpperCase() !== 'POLYGON';
  // The payout is `received / quoted`, and a bridge delivers less than it was
  // given. The backend rewrites net_ngn_kobo to the ACTUAL figure when the
  // deposit is confirmed; until then a bridged sell's net is still the quote,
  // so call it what it is rather than letting the user read it as settled.
  const netIsEstimate =
    isStableAsset &&
    bridgedSource &&
    (intent.status === 'pending_deposit' || intent.status === 'deposit_reported');

  // The resume affordance shows when the status is still pending_deposit and
  // no unreported hash is sitting in storage. That is NOT the same as "nothing
  // was sent" — the write can have failed, the read can have failed, storage
  // can have been cleared, and a bridged send is in flight for minutes — so
  // the copy below offers the option without asserting that the funds stayed
  // put.
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

  // The backend never rewrites ngn_kobo on the buy leg — the claim map writes
  // received_amount, asset_amount, locked_rate and rate_minor, so after an
  // under- or overpayment the quoted naira and the repriced "You get" are
  // mutually inconsistent. received_amount is documented for exactly this
  // ("buy: the FIAT MINOR UNITS the user actually paid", it_models.go:195-205).
  // Show the quote while it IS still a quote, the actual figure once there is
  // one, and only relabel to "You paid" when it is genuinely the latter.
  const buyPaidMinor = isStableBuy && !isAwaitingPayment(intent.status) ? intent.receivedAmount : undefined;
  // Everything on an unpaid (pending_payment or opening) buy is provisional: the binding price is
  // struck when the naira lands, and settlement rewrites both the asset amount
  // and the rate. Confirm already renders "≈"; dropping it here made the same
  // number read as settled one tap later, then visibly change.
  // A routed buy inside its rate hold is firm, not an estimate (spec §5.2).
  const honour = honourOf(intent);
  const buyIsEstimate = isStableBuy && isBuyEstimate(intent.status, honour.holding);
  // The quoted breakdown (spec §14): the display rate in the Rate row, each
  // provider fee on its own line. Empty for everything but a routed buy.
  const buyFeeLines = isStableBuy
    ? intentFeeLines([...(intent.fees ?? []), ...(conversion?.fee ? [conversion.fee] : [])])
    : [];
  // A converted buy's USDC leg is internal; the order screen has the delivery.
  const explorerUrl = isStableBuy && !conversion ? deliveryExplorerUrl(intent) : undefined;
  const conversionGet = conversion
    ? conversion.promisedAmount
      ? `${formatPromisedAmount(conversion) ?? '—'} ${conversion.requestedToken}`
      : (formatEstimate(conversion) ?? `— ${conversion.requestedToken}`)
    : null;
  const conversionChain = conversion
    ? (getChainDisplay(conversion.requestedChain as ChainKey)?.name ?? conversion.requestedChain)
    : null;

  // "Transfer ANY amount" is true for an MCGP buy — that leg reprices whatever
  // lands. It is false for a stablecoin buy: settleStableBuyFromCharge prices
  // what landed and fails closed on an unusable rate OR an underpayment below
  // the provider floor alike, parking the intent in buy_awaiting_rate — an
  // operator-only state with no user-facing recovery (spec §10). Telling a
  // stablecoin buyer to send any amount instructs them into it.
  // Same breakdown the buy screen showed when the order opened, so the fee doesn't vanish on return.
  const mcgpBuyFees = isBuy && !isStableAsset ? buyFeeBreakdown(intent) : null;
  const exactNaira = formatKobo(intent.ngnKobo);
  const stableBuyPayCopy =
    exactNaira === '—'
      ? 'Transfer the exact amount of this purchase into your Instant Transfer account. Sending less can hold your purchase for review.'
      : `Transfer exactly ${exactNaira} into your Instant Transfer account. Sending less can hold your purchase for review.`;

  const awaitingTransfer = isBuy && intent.status === 'pending_payment';
  // An order that ended without delivering never "receives" anything: it
  // shows what was quoted, and the progress steps go unless money moved.
  const ended = ['cancelled', 'failed', 'refunded', 'expired', 'reversed'].includes(intent.status);
  const paidSomething = Number(buyPaidMinor ?? 0) > 0;
  const receiveLabel = ended ? 'Quoted' : 'You receive';
  // Until the API maps ending states itself, its catch-all would send her to
  // support over an order she cancelled.
  const note =
    ended && intent.failureReason?.startsWith('Something went wrong')
      ? intent.status === 'cancelled'
        ? 'This order was cancelled.'
        : intent.failureReason
      : intent.failureReason;
  // A converted buy's intent.destAddress is the treasury leg, never hers.
  const walletShown = conversion ? conversion.destinationAddress || conversion.walletAddress : intent.destAddress;
  const getSymbol = conversion?.requestedToken ?? (isStableAsset ? intent.asset ?? '' : 'MCGP');
  const getChainKey = (conversion?.requestedChain ?? intent.destChain ?? (isStableAsset ? '' : 'sonic')).toLowerCase();
  const getChainName = conversionChain ?? (getChainKey ? getChainDisplay(getChainKey as ChainKey)?.name ?? getChainKey : '');
  // The one line that says what this order is for.
  const headline = isBuy
    ? isStableAsset
      ? conversionGet ?? `${buyIsEstimate ? '≈ ' : ''}${fmtAsset(intent.assetAmount)} ${intent.asset}`
      : `${mcgpBuyFees?.hasFees ? mcgpBuyFees.netMcgp : fmtMcgp(intent.mcgpAmount)} MCGP`
    : isStableAsset
      ? formatKobo(intent.netNgnKobo ?? intent.netMinor ?? intent.ngnKobo)
      : `₦${fmtNaira(intent.ngnKobo)}`;
  const rateLine = isStableAsset
    ? `${buyIsEstimate ? '≈ ' : ''}${formatKobo(isStableBuy ? intentDisplayRateMinor(intent) : intent.rateKobo)} per ${intent.asset}`
    : `₦${fmtRate(intent.rateKobo)} per MCGP`;
  const copyAmount = async () => {
    const naira = (Number(intent.ngnKobo) / 100).toString();
    if (!Number.isFinite(Number(naira))) return;
    await Clipboard.setStringAsync(naira);
    setAmountCopied(true);
    setTimeout(() => setAmountCopied(false), 2000);
  };

  // Everything secondary, behind "Details": the full breakdown, the fee lines,
  // the explorer link and the full wallet address.
  const details = (
    <View style={{ marginTop: spacing.md, gap: spacing.md }}>
      <SummaryCard>
        <SummaryRow label="Status" value={displayStatus(intent.status)} />
        <Divider spacing="xs" />
        {isStableAsset && isBuy ? (
          <>
            <SummaryRow
              label={paidSomething ? 'You paid' : ended ? 'Order amount' : 'You pay'}
              value={formatKobo(paidSomething ? buyPaidMinor : intent.ngnKobo)}
            />
            <Divider spacing="xs" />
            <SummaryRow label={ended ? 'Quoted' : 'You get'} value={headline} />
          </>
        ) : isStableAsset ? (
          <>
            <SummaryRow label="You sell" value={`${fmtAsset(intent.assetAmount)} ${intent.asset}`} />
            <Divider spacing="xs" />
            {/* NET, never ngnKobo (the gross valuation, never updated) — the
                same net → netMinor → gross fallback the sell screens use. */}
            <SummaryRow
              label={ended ? receiveLabel : netIsEstimate ? 'You receive (estimate)' : 'You receive'}
              value={formatKobo(intent.netNgnKobo ?? intent.netMinor ?? intent.ngnKobo)}
            />
          </>
        ) : isBuy ? (
          <>
            <SummaryRow label="You pay" value={`₦${fmtNaira(intent.ngnKobo)}`} />
            {mcgpBuyFees?.hasFees ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="You pay for" value={`${mcgpBuyFees.grossMcgp} MCGP`} />
                <SummaryRow label="Fees" value={`−${mcgpBuyFees.feeMcgp} MCGP`} hint={`≈ ₦${mcgpBuyFees.feeNgn}`} />
              </>
            ) : null}
            <Divider spacing="xs" />
            <SummaryRow label={receiveLabel} value={headline} />
          </>
        ) : (
          <>
            <SummaryRow label="You sell" value={`${fmtMcgp(intent.mcgpAmount)} MCGP`} />
            <Divider spacing="xs" />
            <SummaryRow label={receiveLabel} value={`₦${fmtNaira(intent.ngnKobo)}`} />
          </>
        )}
        {getChainName ? (
          <>
            <Divider spacing="xs" />
            <SummaryRow label="On" value={getChainName} />
          </>
        ) : null}
        <Divider spacing="xs" />
        <SummaryRow label="Rate" value={rateLine.replace(' per ', ' / ')} />
        <Divider spacing="xs" />
        <SummaryRow label="Created" value={shortDate(intent.createdAt)} />
        {intent.txHash ? (
          <>
            <Divider spacing="xs" />
            <SummaryRow label="Tx" value={`${intent.txHash.slice(0, 10)}…${intent.txHash.slice(-6)}`} />
          </>
        ) : null}
      </SummaryCard>
      {buyFeeLines.length ? (
        // "You get" above is already after every one of these.
        <FeeLines fees={buyFeeLines} token={intent.asset ?? ''} symbol="₦" />
      ) : null}
      {isBuy && isStableAsset && walletShown ? (
        <View>
          {/* FULL address, never truncated, and never inside SummaryRow
              (which ellipsises): a truncated address cannot be verified. */}
          <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
            Sent to your wallet
          </Text>
          <Text
            selectable
            numberOfLines={0}
            style={{
              color: colors.textPrimary,
              fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
              fontSize: 13,
              lineHeight: 18,
            }}
          >
            {walletShown}
          </Text>
        </View>
      ) : null}
      {explorerUrl ? (
        <Button title="View on explorer" variant="secondary" onPress={() => { void Linking.openURL(explorerUrl); }} />
      ) : null}
    </View>
  );

  // "You get X on CHAIN · rate", with the breakdown one tap away.
  const summaryCard = (
    <Card>
      <PressableRow onPress={() => setDetailsOpen((v) => !v)}>
        <TokenAvatar symbol={getSymbol} chainKey={(getChainKey || undefined) as never} size={36} />
        <View style={{ flex: 1 }}>
          <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
            {ended
              ? `Quoted ${headline}`
              : isBuy
                ? `You ${awaitingTransfer ? 'get' : 'receive'} ${headline}`
                : `You receive ${headline}`}
          </Text>
          <Text variant="caption" style={{ color: colors.textMuted }}>
            {[getChainName ? `on ${getChainName}` : '', rateLine].filter(Boolean).join(' · ')}
          </Text>
        </View>
        <Text variant="bodySm" weight="600" style={{ color: colors.accent }}>
          {detailsOpen ? 'Hide' : 'Details'}
        </Text>
      </PressableRow>
      {detailsOpen ? details : null}
    </Card>
  );

  const statusChip = (label: string, tone: 'wait' | 'ok' | 'bad') => (
    <View
      style={{
        alignSelf: 'flex-start',
        paddingHorizontal: spacing.sm,
        paddingVertical: 2,
        borderRadius: radius.pill,
        backgroundColor: tone === 'ok' ? colors.successBg : tone === 'bad' ? colors.dangerBg : colors.accentMuted,
      }}
    >
      <Text variant="caption" weight="700" style={{ color: tone === 'ok' ? colors.success : tone === 'bad' ? colors.danger : colors.accent }}>
        {label}
      </Text>
    </View>
  );

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        {awaitingTransfer ? (
          // Step 1 — pay. The amount and where to send it lead; the action
          // bar is pinned below the scroll.
          <Card>
            <Text variant="overline" style={{ color: colors.textMuted }}>
              Transfer exactly
            </Text>
            <PressableRow onPress={() => { void copyAmount(); }} style={{ marginTop: spacing.xs, marginBottom: spacing.md }}>
              <Text variant="display" weight="800" style={{ color: colors.textPrimary }}>
                {exactNaira}
              </Text>
              <Text variant="caption" weight="700" style={{ color: colors.accent }}>
                {amountCopied ? 'Copied' : 'Copy'}
              </Text>
            </PressableRow>
            {payTo ? (
              <PayIntoCard payTo={payTo} />
            ) : (
              <Text variant="bodySm" style={{ color: colors.textMuted }}>
                This order updates on its own once your payment is confirmed.
              </Text>
            )}
            {isStableBuy ? (
              <HonourNotice
                honourUntil={honour.honourUntil}
                pending={intent.status === 'pending_payment'}
                rateUpdated={honour.rateUpdated}
                receive={`${fmtAsset(intent.assetAmount)} ${intent.asset}`}
              />
            ) : null}
            {isStableBuy ? (
              <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
                {stableBuyPayCopy}
              </Text>
            ) : null}
          </Card>
        ) : (
          // Step 2 — track. Progress is the main thing once money has moved.
          <Card>
            {statusChip(
              displayStatus(intent.status),
              ['cancelled', 'failed', 'refunded', 'expired'].includes(intent.status)
                ? 'bad'
                : (isStableAsset ? (isBuy ? isTerminalBuyStatus(intent.status) : isTerminalStableStatus(intent.status)) : intent.status === 'completed')
                  ? 'ok'
                  : 'wait'
            )}
            <Text variant="title1" weight="800" style={{ color: colors.textPrimary, marginTop: spacing.sm }}>
              {ended ? `Order ${displayStatus(intent.status).toLowerCase()}` : headline}
            </Text>
            {ended ? (
              <Text variant="bodySm" style={{ color: colors.textMuted }}>
                {`Was for ${headline}${isBuy && getChainName ? ` on ${getChainName}` : ''}`}
              </Text>
            ) : getChainName ? (
              <Text variant="bodySm" style={{ color: colors.textMuted }}>
                {isBuy ? `to your wallet on ${getChainName}` : `from ${getChainName}`}
              </Text>
            ) : null}
            {isStableAsset && !(ended && isBuy && !paidSomething) ? (
              <View style={{ marginTop: spacing.lg }}>
                <Stepper steps={hops} />
                {conversion ? (
                  <ConversionProgress
                    conversion={conversion}
                    paid={!isAwaitingPayment(intent.status) && intent.status !== 'cancelled'}
                  />
                ) : null}
              </View>
            ) : null}
            {note ? (
              <View style={{ marginTop: spacing.md }}>
                <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                  Note
                </Text>
                <Text variant="bodySm" style={{ color: colors.textPrimary }}>
                  {note}
                </Text>
              </View>
            ) : null}
            {isStableBuy && !ended ? (
              <HonourNotice
                honourUntil={honour.honourUntil}
                pending={false}
                rateUpdated={honour.rateUpdated}
                receive={`${fmtAsset(intent.assetAmount)} ${intent.asset}`}
              />
            ) : null}
          </Card>
        )}

        {summaryCard}

        {showResume ? (
          canResumeDirect ? (
            <Card>
              <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
                Haven&apos;t sent yet?
              </Text>
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
                Send exactly {fmtAsset(intent.assetAmount)} {intent.asset} on Polygon to this address
                to continue this order. If you have already sent, do not send again — report it
                below instead.
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
                Haven&apos;t sent yet?
              </Text>
              {/* NOT "this is a harmless, unused record". The app cannot know
                  that: the hash write can have failed, the read can have
                  failed, storage can have been cleared, and a bridged transfer
                  sits in flight for minutes while this order stays at
                  pending_deposit. A user with funds mid-bridge who reads
                  "unused record" starts a second sell — two sends, one intent
                  each, and an orphan deposit to reconcile by hand. */}
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
                If you have not sent anything yet, you can start a new sell. If you have already
                sent, do not send again — report your transaction below, or contact support with
                the hash.
              </Text>
              <Button
                title="Start again"
                variant="secondary"
                onPress={() =>
                  router.push(`/wallet/paycrest/sell?asset=${intent.asset}` as never)
                }
              />
            </Card>
          )
        ) : null}

        {/* Manual recovery. Offered whenever the deposit has not been recorded
            yet — a lost hash is the one failure this flow cannot undo for the
            user, and on a bridged sell there is no history lookup to fall back
            on. Report is idempotent per (intent, hash).
            SELL only — `!isBuy` — because "deposit" here means the user's own
            on-chain send to Flutterwave, which has no equivalent on a
            stablecoin buy. Without this gate a buy intent's own statuses
            (payment_received, awaiting_funds, delivering, …) are all absent
            from isTerminalStableStatus's set, so this card would show a
            "paste your transaction hash" prompt to a buy that has no
            transaction to paste. */}
        {isStableAsset && !isBuy && !isTerminalStableStatus(intent.status) && !intent.depositTxHash ? (
          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
              Already sent it?
            </Text>
            {manualHashDone ? (
              <Text variant="bodySm" style={{ color: colors.success }}>
                Thanks — we have your transaction and are checking for it now.
              </Text>
            ) : (
              <>
                <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
                  Paste the transaction hash from your wallet and we will match it to this order.
                </Text>
                <Input
                  value={manualHash}
                  onChangeText={(v) => {
                    setManualHash(v);
                    setManualHashError(null);
                  }}
                  placeholder="0x…"
                  autoCapitalize="none"
                  autoCorrect={false}
                  error={manualHashError ?? undefined}
                  containerStyle={{ marginBottom: spacing.md }}
                />
                <Button
                  title="Report this transaction"
                  variant="secondary"
                  loading={reportingManual}
                  disabled={!manualHash.trim()}
                  onPress={submitManualHash}
                />
              </>
            )}
          </Card>
        ) : null}
        {/* Same queue as ramp orders and bills. Kept off intents still waiting
            on the user's money — there is nothing to dispute until something
            has been sent, and the server refuses those anyway. */}
        <DisputeSection
          subjectType="it_intent"
          subjectId={intent.id}
          disputable={
            !isAwaitingPayment(intent.status) &&
            intent.status !== 'pending_deposit' &&
            intent.status !== 'cancelled'
          }
        />
      </ScrollView>
      {awaitingTransfer ? (
        <View
          style={{
            paddingHorizontal: spacing.lg,
            paddingTop: spacing.md,
            paddingBottom: spacing.sm,
            gap: spacing.xs,
            backgroundColor: colors.surface,
            borderTopWidth: 1,
            borderTopColor: colors.border,
          }}
        >
          <Button title="I've sent the transfer" loading={checkingPayment} onPress={() => { void checkPayment(); }} />
          {paymentCheckNote ? (
            <Text variant="caption" style={{ color: colors.textMuted, textAlign: 'center' }}>
              {paymentCheckNote}
            </Text>
          ) : null}
          {cancelBuyError ? (
            <Text variant="caption" style={{ color: colors.danger, textAlign: 'center' }}>
              {cancelBuyError}
            </Text>
          ) : null}
          <View style={{ flexDirection: 'row', justifyContent: 'center', gap: spacing.lg }}>
            <Button title="Paid from another account?" variant="ghost" size="sm" fullWidth={false} onPress={() => setPayerSheetOpen(true)} />
            <Button
              title={isStableAsset ? 'Cancel purchase' : 'Cancel order'}
              variant="ghost"
              size="sm"
              fullWidth={false}
              loading={cancellingBuy}
              onPress={handleCancelBuy}
            />
          </View>
        </View>
      ) : null}
      <PayerDeclarationSheet
        visible={payerSheetOpen}
        orderId={intent.id}
        onClose={() => setPayerSheetOpen(false)}
        onDeclared={(d) => {
          // A match settles the order: re-read it. Otherwise the screen keeps
          // polling and settles on its own when the payment lands.
          if (d.status === 'matched') void load();
          else
            setPaymentCheckNote(
              d.status === 'waiting'
                ? "Got it. We'll match the payment from that account as soon as it arrives."
                : 'Got it. Our team will check the payment and confirm your order shortly.'
            );
        }}
      />
    </SafeAreaView>
  );
}
