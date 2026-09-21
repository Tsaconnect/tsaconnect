// app/wallet/paycrest/sell.tsx
//
// Sell stablecoins for Naira through Paycrest's provider network.
//
// Non-custodial: the user sends the stablecoin FROM their own connected wallet
// to the provider's address, and the provider pays their bank directly. TSA
// never holds the asset or the Naira — which also means we cannot reverse a
// send, so the deposit address and amount are shown copyably and never
// truncated.
//
// The user types the Naira they want to receive; the token amount is derived
// from the live quote. That is the right way round for this market — people
// think in "I need ₦50,000", not "I'll sell 36.4 USDC".
//
// Flow: amount -> live routes -> payout bank -> create -> send from the app
// (or, for funds held elsewhere, the deposit address) -> the order screen.
//
// Funding is checked BEFORE the order is created, not at the send: an order
// carries a countdown, and starting one against a route the wallet cannot
// cover burns that window on a sale that was never going to complete. See
// services/paycrestDeposit.ts#checkFunding.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  CopyableField,
  Input,
  PickerSheet,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import { DepositProgress } from '@/components/paycrest/DepositProgress';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import { useRampCurrency } from '@/hooks/useRampCurrency';
import { resolveAccountName } from '@/services/ngBanks';
import {
  accountFieldLabel,
  accountFieldPlaceholder,
  accountValueSurvivesKindChange,
  accountNumberMaxLength,
  isDigitsOnlyAccountField,
  isValidAccountNumber,
  sanitizeAccountNumberInput,
} from '@/utils/payoutAccount';
import {
  cancelOrder,
  claimPayment,
  createSell,
  getPayoutInstitutions,
  getQuote,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type PayoutInstitution,
} from '@/services/paycrestApi';
import {
  balanceOf,
  checkFunding,
  sendSponsoredPaycrestDeposit,
  type FundingCheck,
} from '@/services/paycrestDeposit';
import {
  getSponsoredRoutes,
  getSponsorshipOffer,
  isGasSponsored,
  type SponsorRoute,
} from '@/services/gasSponsorship';
import {
  getWalletBalances,
  normalizeWalletBalances,
  type WalletBalanceWithChain,
} from '@/services/walletApi';
import { CHAINS, type ChainKey } from '@/constants/chains';

const QUOTE_DEBOUNCE_MS = 450;
/** Warn while the user can still act. An on-chain send needs gas, a signature
 *  and confirmations — more steps than a bank transfer, so warn earlier. */
const EXPIRY_WARNING_SECONDS = 420;

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

const venueId = (v: PaycrestVenue) => `${v.token}:${v.chain}`;
const venueLabel = (v: PaycrestVenue) => `${v.token} on ${CHAIN_LABELS[v.chain] ?? v.chain}`;
const fmtTokenAmount = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 2 : 6 });

/** The trailing half of a route's sublabel — silent when the route is fine. */
function routeNote(check: FundingCheck): string {
  if (check.ok) return '';
  if (check.reason === 'short-token') return ` · you have ${check.have}`;
  if (check.reason === 'no-gas') return ` · no ${check.nativeSymbol} for gas`;
  return ' · not supported in-app';
}

/** The wallet's gas balance on a chain, or null when balances aren't known. */
function nativeBalanceFor(chain: string, balances: WalletBalanceWithChain[] | null): number | null {
  const symbol = CHAINS[chain as ChainKey]?.nativeCurrency.symbol;
  if (!symbol || !balances) return null;
  const row = balances.find((b) => b.chainKey === chain && b.symbol === symbol);
  const n = Number(row?.balance ?? 0);
  return Number.isFinite(n) ? n : 0;
}

/** The same verdict as a full sentence, for the form. */
function fundingMessage(check: FundingCheck): string | null {
  if (check.ok) return null;
  const chain = CHAIN_LABELS[check.chain] ?? check.chain;
  if (check.reason === 'short-token') {
    return `You have ${check.have} ${check.token} on ${chain} — this sale needs ${check.need}. Pick another network or top up.`;
  }
  if (check.reason === 'no-gas') {
    return `You have no ${check.nativeSymbol} on ${chain} to pay the network fee. Pick another network or add some ${check.nativeSymbol}.`;
  }
  return `This app cannot send on ${chain} yet — choose another network, or send from an external wallet.`;
}

export default function PaycrestSellScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  const { asset, currency: currencyParam } = useLocalSearchParams<{
    asset?: string;
    currency?: string;
  }>();

  // The currency this order settles in — NOT the display currency. See
  // hooks/useRampCurrency.ts: deriving this from CurrencyContext's display
  // preference is the exact regression documented at wallet/it/index.tsx (a
  // user who switched display to USD lost the working NGN ramp). Restricted
  // to 'paycrest' so the picker can never offer a currency this screen would
  // fail to quote.
  const {
    code: currencyCode,
    currency: currencyConfig,
    setCode: setCurrencyCode,
    options: currencyOptions,
  } = useRampCurrency('paycrest', 'sell');
  const symbol = currencyConfig.symbol;
  const currencyLabel = !currencyCode || currencyCode === 'NGN' ? 'Naira' : currencyCode;
  const headerTitle = `Sell for ${currencyLabel}`;
  useEffect(() => {
    if (currencyParam) setCurrencyCode(currencyParam);
    // One-time sync of the hub's choice on arrival — see the identical note
    // on the buy screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currencyParam]);
  const [currencyPickerOpen, setCurrencyPickerOpen] = useState(false);

  // resolveAccountName is a Nigerian NUBAN lookup — it cannot resolve an
  // account in any other currency's country, so gate both the lookup and the
  // submit requirement on NGN or sell becomes permanently stuck outside it.
  const requiresNameCheck = currencyCode === 'NGN';

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [routeOpen, setRouteOpen] = useState(false);

  // Payout destination.
  const [banks, setBanks] = useState<PayoutInstitution[]>([]);
  const [bank, setBank] = useState<PayoutInstitution | null>(null);
  const [bankOpen, setBankOpen] = useState(false);
  // What the chosen destination actually is. Paycrest reports it per
  // institution — every UGX option is mobile money, KES and TZS carry a few
  // among their banks — and it decides what the account field asks for. An
  // API build that does not send it reads as a bank, the majority everywhere.
  const destKind = bank?.type ?? 'bank';
  const [accountNo, setAccountNo] = useState('');
  const [accountName, setAccountName] = useState('');
  const [resolving, setResolving] = useState(false);

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [creating, setCreating] = useState(false);

  // The in-app send.
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [manualOpen, setManualOpen] = useState(false);
  const [cancelling, setCancelling] = useState(false);

  // Every chain's balances in one request — the un-filtered endpoint already
  // fans out server-side, so annotating all five routes costs no more than
  // checking one. null means "not known", which never blocks anything.
  const [balances, setBalances] = useState<WalletBalanceWithChain[] | null>(null);

  const quoteSeq = useRef(0);

  // An offramp expires exactly like an onramp does. Sending after the window
  // closes puts crypto at a provider address for an order that no longer
  // exists — the same stranded-funds failure as a late bank transfer, and just
  // as unrecoverable, so the deadline has to be on screen.
  const { label: countdown, expired, secs } = useCountdown(order?.validUntil);
  const expiringSoon = !expired && secs > 0 && secs <= EXPIRY_WARNING_SECONDS;

  // Balances for the signable wallet, fetched once it is known. A failure
  // leaves `balances` null and every funding check answers "ok" — see
  // checkFunding: a flaky indexer must not block a sale the user can fund.
  useEffect(() => {
    if (!address) return;
    let cancelled = false;
    getWalletBalances(undefined, address)
      .then((res) => {
        if (cancelled) return;
        setBalances(res.success ? normalizeWalletBalances(res.data) : null);
      })
      .catch(() => {
        if (!cancelled) setBalances(null);
      });
    return () => {
      cancelled = true;
    };
  }, [address]);

  // Routes this user sells on without holding gas. An empty list — sponsorship
  // off, an older server, a network error — keeps today's gas warning.
  const [sponsoredRoutes, setSponsoredRoutes] = useState<SponsorRoute[]>([]);
  useEffect(() => {
    let cancelled = false;
    getSponsoredRoutes().then((routes) => {
      if (!cancelled) setSponsoredRoutes(routes);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  // The routes list is a pre-order hint; the per-order offer can still come
  // back 'none' (domain unverified, too little time left, lane switched off),
  // and then the send self-pays. Only claim the fee is covered when it is.
  const [orderSponsored, setOrderSponsored] = useState(false);
  useEffect(() => {
    if (!order?.id) {
      setOrderSponsored(false);
      return;
    }
    let cancelled = false;
    getSponsorshipOffer(order.id)
      .then((offer) => {
        if (!cancelled) setOrderSponsored(offer.mode !== 'none');
      })
      .catch(() => {
        if (!cancelled) setOrderSponsored(false);
      });
    return () => {
      cancelled = true;
    };
  }, [order?.id]);

  // The server returns the list already in the right vocabulary for this
  // currency, so there is nothing to intersect. Outside Nigeria there is no
  // local list to intersect WITH. The previously selected bank is cleared on
  // a currency change — it belongs to the old currency's list and may not
  // even exist in the new one.
  useEffect(() => {
    setBank(null);
    getPayoutInstitutions(currencyCode)
      .then(setBanks)
      .catch(() => setBanks([]));
  }, [currencyCode]);

  // Live quoting, same contract as the buy screen: a quote is only true for
  // the size it was asked about, and only for a moment.
  useEffect(() => {
    const raw = amount.trim();
    if (!raw || Number(raw) <= 0) {
      setQuote(null);
      setVenue(null);
      setQuoteError(null);
      return;
    }
    const seq = ++quoteSeq.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      try {
        const q = await getQuote('sell', raw, currencyCode);
        if (seq !== quoteSeq.current) return;
        setQuote(q);
        setQuoteError(null);
        setVenue((prev) => {
          const kept = prev && q.venues.find((v) => venueId(v) === venueId(prev));
          const preferred = asset
            ? q.venues.find((v) => v.token.toUpperCase() === asset.toUpperCase())
            : undefined;
          return kept ?? preferred ?? q.best ?? null;
        });
      } catch (e) {
        if (seq !== quoteSeq.current) return;
        setQuote(null);
        setVenue(null);
        setQuoteError(
          e instanceof PaycrestError ? e.message : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === quoteSeq.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [amount, asset, currencyCode]);

  // Resolve the account name so the user sees who they are actually paying
  // before any crypto leaves their wallet.
  useEffect(() => {
    if (!requiresNameCheck || !bank || !isValidAccountNumber(currencyCode, accountNo, destKind)) {
      setAccountName('');
      return;
    }
    let cancelled = false;
    setResolving(true);
    (async () => {
      try {
        const res = await resolveAccountName(bank.code, accountNo);
        if (cancelled) return;
        setAccountName(res.ok ? res.accountName : '');
      } catch {
        if (!cancelled) setAccountName('');
      } finally {
        if (!cancelled) setResolving(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // destKind belongs here: switching a Nigerian destination between a bank
    // and mobile money changes whether the entered number is valid, and a
    // stale resolved name would then be attached to the wrong destination.
  }, [requiresNameCheck, bank, accountNo, currencyCode, destKind]);

  const onConfirm = useCallback(async () => {
    if (!venue || !address || !bank) return;
    setCreating(true);
    try {
      const created = await createSell({
        amount: amount.trim(),
        chain: venue.chain,
        token: venue.token,
        walletAddress: address,
        bankCode: bank.code,
        accountNumber: accountNo,
        accountName,
        currency: currencyCode,
      });
      setOrder(created);
    } catch (e) {
      Alert.alert(
        'Could not start your sale',
        e instanceof PaycrestError ? e.message : 'Please try again.',
      );
    } finally {
      setCreating(false);
    }
  }, [venue, address, bank, accountNo, accountName, amount, currencyCode]);

  const venueBalance =
    venue && balances ? balanceOf(balances, venue.chain, venue.token) : null;

  const funding: FundingCheck | null = useMemo(
    () =>
      venue
        ? checkFunding(venue, balances, {
            gasSponsored: isGasSponsored(sponsoredRoutes, venue.chain, venue.token),
          })
        : null,
    [venue, balances, sponsoredRoutes],
  );

  /** Hand the order off to the order screen, as "I've sent it" always has.
   *
   *  `sentTx` carries the deposit we just broadcast, so the order screen shows
   *  progress instead of re-asking for a transfer that has already left. It is
   *  route state, so it does not survive the app being killed — the order
   *  screen falls back to the server's paymentClaimedAt for that. */
  const goToOrder = useCallback((id: string, sentTx?: string) => {
    // Tell the server the user asserts they sent the deposit — the only
    // signal that lets an order which later expires with amountPaid=0 read as
    // a genuine unmatched-deposit alarm instead of an ordinary abandoned
    // checkout. Fire-and-forget: the send already happened, so a failed claim
    // must not block navigation — it just means this order degrades to a
    // plain "expired" if it never matches, the same as before.
    claimPayment(id).catch(() => {});
    const to = sentTx
      ? `/wallet/paycrest/order/${id}?sentTx=${encodeURIComponent(sentTx)}`
      : `/wallet/paycrest/order/${id}`;
    router.replace(to as never);
  }, []);

  const onSend = useCallback(async () => {
    if (!order || !address) return;
    setSending(true);
    setSendError(null);
    try {
      const txHash = await sendSponsoredPaycrestDeposit(
        order,
        address,
        nativeBalanceFor(order.chain, balances),
      );
      goToOrder(order.id, txHash);
    } catch (e) {
      // Server and RPC messages here are written to be read — show them.
      setSendError(e instanceof Error ? e.message : 'Could not send. Please try again.');
    } finally {
      setSending(false);
    }
  }, [order, address, balances, goToOrder]);

  // Backing out here is the common case — the user got to the deposit screen
  // and changed their mind. Without this the abandoned order holds the
  // server's one-open-order lock until its window closes, so they cannot start
  // the sale they actually want. See PaycrestOrderService.Cancel.
  const confirmCancel = useCallback(() => {
    if (!order) return;
    Alert.alert(
      'Cancel this sale?',
      'Only cancel if you have NOT sent the crypto yet. If you have already sent it, do not cancel — contact support instead.',
      [
        { text: 'Keep it', style: 'cancel' },
        {
          text: 'Cancel sale',
          style: 'destructive',
          onPress: async () => {
            setCancelling(true);
            try {
              await cancelOrder(order.id);
              // Back to the form with the lock released, rather than to a dead
              // order screen the user would only have to leave again.
              setOrder(null);
              setSendError(null);
              setManualOpen(false);
            } catch (e) {
              Alert.alert(
                'Could not cancel',
                e instanceof PaycrestError ? e.message : 'Please try again.',
              );
            } finally {
              setCancelling(false);
            }
          },
        },
      ],
    );
  }, [order]);

  const routeItems = useMemo(
    () =>
      (quote?.venues ?? []).map((v) => {
        const send = `Send ${v.tokenAmount.toFixed(4)} ${v.token}`;
        const check = checkFunding(v, balances, {
          gasSponsored: isGasSponsored(sponsoredRoutes, v.chain, v.token),
        });
        // Routes stay selectable when they are short — the user may top up,
        // and the reason is spelled out on the form once one is chosen. The
        // point of the annotation is that the user can see which route they
        // CAN take without poking each one blind.
        return { id: venueId(v), label: venueLabel(v), sublabel: `${send}${routeNote(check)}` };
      }),
    [quote, balances, sponsoredRoutes],
  );

  const bankItems = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name })),
    [banks],
  );

  // A funding shortfall only blocks the DEFAULT path. Someone selling from
  // MetaMask or an exchange has a perfectly good order to create and an app
  // balance of zero — blocking them outright would be a regression, so the
  // warning carries its own escape hatch and resets whenever the route
  // changes (the next route's verdict is a different question).
  const [fundingOverride, setFundingOverride] = useState(false);
  useEffect(() => setFundingOverride(false), [venue?.chain, venue?.token]);
  const fundingBlocks =
    !fundingOverride &&
    !!funding &&
    !funding.ok &&
    (funding.reason === 'short-token' || funding.reason === 'no-gas');

  const ready =
    !!venue &&
    !!address &&
    !!bank &&
    isValidAccountNumber(currencyCode, accountNo, destKind) &&
    (!requiresNameCheck || !!accountName) &&
    !fundingBlocks;

  // ── Deposit instructions ────────────────────────────────────────────────
  if (order) {
    // No key on this device, or a chain we cannot build a tx for: the address
    // is the only thing we can offer, so it opens expanded.
    const canSendInApp =
      !!address && !!CHAINS[order.chain as ChainKey] && !!order.depositAddress && !!order.tokenAmount;
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <Stack.Screen options={{ title: headerTitle }} />
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <Card>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              Send exactly
            </Text>
            <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
              {order.tokenAmount} {order.token}
            </Text>
            <Text variant="caption" style={{ color: colors.textMuted }}>
              on {CHAIN_LABELS[order.chain] ?? order.chain}
              {canSendInApp ? ', from your wallet on this device.' : ', from your connected wallet.'}
            </Text>
          </Card>

          <Card
            style={{
              borderWidth: 1,
              borderColor: expired ? colors.danger : expiringSoon ? colors.warning : colors.border,
            }}
          >
            {expired ? (
              <>
                <Text variant="bodyMed" weight="700" style={{ color: colors.danger }}>
                  This order has expired
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Do not send to this address. Start a new sale instead.
                </Text>
              </>
            ) : (
              <>
                <Text
                  variant="bodySm"
                  style={{ color: expiringSoon ? colors.warning : colors.textSecondary }}
                >
                  Time left to send
                </Text>
                <Text
                  variant="title1"
                  weight="700"
                  style={{ color: expiringSoon ? colors.warning : colors.textPrimary }}
                >
                  {countdown}
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Sending after this runs out leaves your crypto at the provider with no order to
                  match it to.
                </Text>
              </>
            )}
          </Card>

          <SummaryCard>
            <SummaryRow label="You receive" value={`${symbol}${order.amount}`} />
            <SummaryRow label="Into" value={order.payoutAccountName ?? ''} />
            <SummaryRow label="Account" value={order.payoutAccount ?? ''} />
            {order.rate ? <SummaryRow label="Rate" value={`${symbol}${order.rate}`} /> : null}
          </SummaryCard>

          {sendError && (
            <Card style={{ borderWidth: 1, borderColor: colors.danger }}>
              <Text variant="bodySm" style={{ color: colors.danger }}>
                {sendError}
              </Text>
            </Card>
          )}

          {/* Once we are broadcasting, the address is the wrong thing to look
              at: the transfer is already leaving. Show what is happening. */}
          {sending && (
            <DepositProgress
              amountLabel={`${order.tokenAmount} ${order.token}`}
              chainLabel={CHAIN_LABELS[order.chain] ?? order.chain}
            />
          )}

          {/* The wallet holding the key is right here — asking the user to
              copy an address into another app was never the shorter path. */}
          {canSendInApp && (
            <Button
              title={`Send ${order.tokenAmount} ${order.token}`}
              onPress={onSend}
              loading={sending}
              disabled={expired || sending}
              fullWidth
            />
          )}

          {canSendInApp && orderSponsored && (
            <Text variant="caption" style={{ color: colors.textMuted, textAlign: 'center' }}>
              Network fee covered — you don&apos;t need{' '}
              {CHAINS[order.chain as ChainKey]?.nativeCurrency.symbol ?? 'gas'} for this sale.
            </Text>
          )}

          {/* Kept for funds that live somewhere else — an exchange, a hardware
              wallet, MetaMask. Collapsed rather than removed, and out of the
              way entirely while we are sending from this device. */}
          {(manualOpen || !canSendInApp) && !sending ? (
            <Card>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
                To this address
              </Text>
              {/* Full address, never truncated: a send to a mistyped address is
                  irreversible and nothing on our side can recover it. */}
              <CopyableField value={order.depositAddress ?? ''} />
              <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
                Send only {order.token} on {CHAIN_LABELS[order.chain] ?? order.chain}. A different
                token, or the right token on a different network, will not reach the provider.
              </Text>
              <View style={{ marginTop: spacing.md }}>
                <Button
                  title="I've sent it"
                  variant={canSendInApp ? 'secondary' : 'primary'}
                  onPress={() => goToOrder(order.id)}
                  disabled={expired}
                  fullWidth
                />
              </View>
            </Card>
          ) : (
            <Button
              title="Send from another wallet"
              variant="ghost"
              onPress={() => setManualOpen(true)}
              fullWidth
            />
          )}

          {/* Hidden once the user says they have sent — cancelling then would
              claim the order is dead while their deposit is still in flight. */}
          {!order.paymentClaimedAt && (
            <Button
              title="Cancel this sale"
              variant="ghost"
              onPress={confirmCancel}
              loading={cancelling}
              disabled={cancelling}
              fullWidth
            />
          )}
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount, route, payout ───────────────────────────────────────────────
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <Stack.Screen options={{ title: headerTitle }} />
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          {/* Only offer a choice when there is one — see the identical note
              on the buy screen. */}
          {currencyOptions.length > 1 && (
            <SelectRow
              label="Currency"
              value={currencyCode}
              onPress={() => setCurrencyPickerOpen(true)}
            />
          )}

          <AmountInput
            label="You receive"
            value={amount}
            onChangeText={setAmount}
            symbol={symbol}
            symbolPosition="prefix"
            autoFocus
            // Hidden while re-quoting: the previous route's figure belongs to
            // the previous amount.
            helper={
              venue && !quoting
                ? `≈ ${fmtTokenAmount(venue.tokenAmount)} ${venue.token}`
                : undefined
            }
          />

          {quoting && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Spinner size="small" />
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Checking live rates…
              </Text>
            </View>
          )}

          {quoteError && !quoting && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
              <Text variant="bodySm" style={{ color: colors.warning }}>
                {quoteError}
              </Text>
            </Card>
          )}

          {quote && quote.venues.length > 0 && (
            <View style={{ gap: spacing.xs }}>
              <SelectRow
                label="You send"
                value={venue ? venueLabel(venue) : undefined}
                placeholder="Choose a network"
                onPress={() => setRouteOpen(true)}
              />
              {/* Unknown balances show nothing rather than a misleading 0. */}
              {venue && venueBalance !== null && (
                <Text
                  variant="caption"
                  style={{
                    color: venueBalance < venue.tokenAmount ? colors.warning : colors.textMuted,
                    textAlign: 'right',
                  }}
                >
                  Balance: {fmtTokenAmount(venueBalance)} {venue.token}
                </Text>
              )}
            </View>
          )}

          <SelectRow
            label="Pay into"
            value={bank?.name}
            placeholder="Choose a destination"
            onPress={() => setBankOpen(true)}
          />
          {/* The field follows the DESTINATION, not the currency: pick MTN in
              Uganda and this asks for a phone number, pick a bank and it asks
              for an account number. */}
          <Input
            label={accountFieldLabel(destKind)}
            value={accountNo}
            onChangeText={(v: string) =>
              setAccountNo(sanitizeAccountNumberInput(currencyCode, v, destKind))
            }
            keyboardType={
              isDigitsOnlyAccountField(currencyCode, destKind) ? 'number-pad' : 'default'
            }
            placeholder={accountFieldPlaceholder(currencyCode, destKind)}
            maxLength={accountNumberMaxLength(currencyCode, destKind)}
          />
          {requiresNameCheck && resolving && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Checking account…
            </Text>
          )}
          {requiresNameCheck && !!accountName && (
            <Text variant="bodySm" weight="600" style={{ color: colors.success }}>
              {accountName}
            </Text>
          )}
          {requiresNameCheck &&
            !resolving &&
            !accountName &&
            isValidAccountNumber(currencyCode, accountNo, destKind) &&
            bank && (
              <Text variant="caption" style={{ color: colors.danger }}>
                We could not verify that account. Check the number and bank.
              </Text>
            )}

          {venue && (
            <SummaryCard>
              <SummaryRow
                label="You send"
                value={`${venue.tokenAmount.toFixed(6)} ${venue.token}`}
              />
              <SummaryRow label="Rate" value={`${symbol}${venue.rate} / ${venue.token}`} />
            </SummaryCard>
          )}

          {funding && !funding.ok && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
              <Text variant="bodySm" style={{ color: colors.warning }}>
                {fundingMessage(funding)}
              </Text>
              {fundingBlocks && (
                <Button
                  title="I'll send from another wallet"
                  variant="ghost"
                  size="sm"
                  onPress={() => setFundingOverride(true)}
                />
              )}
            </Card>
          )}

          <Button
            title="Continue"
            onPress={onConfirm}
            loading={creating}
            disabled={!ready || quoting || creating}
            fullWidth
          />
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — you send the crypto yourself.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={currencyPickerOpen}
        onClose={() => setCurrencyPickerOpen(false)}
        title="Currency"
        items={currencyOptions}
        selectedId={currencyCode}
        onSelect={(item) => {
          setCurrencyCode(item.id);
          // A new currency means a fresh route and payout destination — the
          // ones held over would either belong to the wrong currency's quote
          // or (for the bank/account) simply not exist in the new list.
          setAmount('');
          setVenue(null);
          setAccountNo('');
          setAccountName('');
          setCurrencyPickerOpen(false);
        }}
        emptyText="No currencies available."
      />
      <PickerSheet
        visible={routeOpen}
        onClose={() => setRouteOpen(false)}
        title="Choose a network"
        items={routeItems}
        selectedId={venue ? venueId(venue) : undefined}
        onSelect={(item) => {
          const picked = quote?.venues.find((v) => venueId(v) === item.id);
          if (picked) setVenue(picked);
          setRouteOpen(false);
        }}
        emptyText="No routes can fill this amount right now."
      />
      <PickerSheet
        visible={bankOpen}
        onClose={() => setBankOpen(false)}
        title="Choose a destination"
        items={bankItems}
        selectedId={bank?.code}
        searchable
        onSelect={(item) => {
          const next = banks.find((b) => b.code === item.id) ?? null;
          // Switching between kinds invalidates what was typed — a phone
          // number is not an account number, and each passes the other's rule
          // by accident. See accountValueSurvivesKindChange.
          if (!accountValueSurvivesKindChange(destKind, next?.type)) {
            setAccountNo('');
            setAccountName('');
          }
          setBank(next);
          setBankOpen(false);
        }}
        emptyText="No destinations available."
      />
    </SafeAreaView>
  );
}
