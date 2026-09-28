// app/wallet/paycrest/sell.tsx
//
// Sell stablecoins for local fiat (the selected ramp currency) through
// Paycrest's provider network.
//
// Non-custodial: the user sends the stablecoin FROM their own connected wallet
// to the provider's address, and the provider pays their bank directly. TSA
// never holds the asset or the fiat — which also means we cannot reverse a
// send, so the deposit address and amount are shown copyably and never
// truncated.
//
// The user types the fiat they want to receive; the token amount is derived
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
//
// Any other coin she holds on an EVM chain (design §5) is a SWAPPED sell: she
// picks it from her holdings and SwappedSellForm takes over the amount — one
// "~₦" estimate, one Sell tap. useSwappedSell does the rest — swap to USDC on
// Base in her own wallet, open the payout for what landed, send it — and
// "Finish your sale" picks up a sale that stopped part-way. Wallet MCGP is
// never offered (utils/walletMcgp.ts): only Spendable MCGP sells, from the
// MCGP row.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
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
import {
  SWAPPED_STAGE_LABELS,
  SwappedSellForm,
  SwappedSellStatus,
  type SwapPick,
} from '@/components/paycrest/SwappedSellForm';
import AssetPicker from '@/components/assets/AssetPicker';
import { notWalletMcgp } from '@/utils/walletMcgp';
import { canCancelSale, useSwappedSell } from '@/hooks/useSwappedSell';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import { rampCurrencyLabel, useRampCurrency } from '@/hooks/useRampCurrency';
import { useScreenTitle } from '@/hooks/useScreenTitle';
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
  getOrder,
  getPayoutInstitutions,
  getQuote,
  isTerminal,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type PayoutInstitution,
} from '@/services/paycrestApi';
import {
  balanceOf,
  CHAIN_LABELS,
  checkFunding,
  DEPOSIT_OUTCOME_UNKNOWN,
  fmtTokenAmount,
  fundingMessage,
  nativeBalanceFor,
  type FundingCheck,
} from '@/services/paycrestDeposit';
import {
  depositMayBeOut,
  depositOutcomeUnknown,
  sameDeposit,
  sendHeldDeposit,
  stillToSend,
} from '@/services/depositGuard';
import { clearHold, depositHoldKey } from '@/services/payHold';
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
import { getSwappedSellState, type SellIntent } from '@/services/paycrestSellSwap';
import { isBtcLiFiChain, isNonEVMLiFiChain, isSolanaLiFiChain, isSuiLiFiChain, type LiFiToken } from '@/services/lifi';
import { CHAINS, getChainByChainId, type ChainKey } from '@/constants/chains';

const QUOTE_DEBOUNCE_MS = 450;
/** Warn while the user can still act. An on-chain send needs gas, a signature
 *  and confirmations — more steps than a bank transfer, so warn earlier. */
const EXPIRY_WARNING_SECONDS = 420;

const CHECK_FAILED = 'Could not check on your order, so nothing was sent. Please try again.';
const OTHER_WALLET =
  'This sale was started from a different wallet, so nothing was sent. Switch back to that wallet to send it.';
const ORDER_CHANGED =
  'This order changed since you opened it, so nothing was sent. Check the amount and address above before sending.';

const venueId = (v: PaycrestVenue) => `${v.token}:${v.chain}`;
const venueLabel = (v: PaycrestVenue) => `${v.token} on ${CHAIN_LABELS[v.chain] ?? v.chain}`;

/** The trailing half of a route's sublabel — silent when the route is fine. */
function routeNote(check: FundingCheck): string {
  if (check.ok) return '';
  if (check.reason === 'short-token') return ` · you have ${check.have}`;
  if (check.reason === 'no-gas') return ` · no ${check.nativeSymbol} for gas`;
  return ' · not supported in-app';
}

/** Stablecoins this screen already sells directly — only consulted before
 *  any quote has said which routes are live. */
const DIRECT_TOKENS = new Set(['USDC', 'USDT']);

function networkName(chainId: number): string {
  if (isSolanaLiFiChain(chainId)) return 'Solana';
  if (isBtcLiFiChain(chainId)) return 'Bitcoin';
  if (isSuiLiFiChain(chainId)) return 'Sui';
  return getChainByChainId(chainId)?.name ?? 'another network';
}

export default function PaycrestSellScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  const { asset, currency: currencyParam, openPicker } = useLocalSearchParams<{
    asset?: string;
    currency?: string;
    /** Set by the IT hub's "Any other token" row — see the identical param on
     *  the buy screen. */
    openPicker?: string;
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
  const currencyLabel = rampCurrencyLabel(currencyCode);
  useScreenTitle(`Sell for ${currencyLabel}`);
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
  // Whether this order's deposit may already have left this phone (its hold —
  // services/depositGuard). null until read: neither Send nor Cancel is offered
  // on a guess.
  const [held, setHeld] = useState<boolean | null>(null);

  // Every chain's balances in one request — the un-filtered endpoint already
  // fans out server-side, so annotating all five routes costs no more than
  // checking one. null means "not known", which never blocks anything.
  const [balances, setBalances] = useState<WalletBalanceWithChain[] | null>(null);

  const quoteSeq = useRef(0);

  // ── What she is selling ────────────────────────────────────────────────
  // Nothing picked, or a stablecoin a live route takes: today's direct sale.
  // Anything else she holds: the swapped sale.
  const [pickerOpen, setPickerOpen] = useState(false);
  useEffect(() => {
    // Visibility still passes through swappedSellOn below
    // (visible={swappedSellOn && pickerOpen}), so this cannot show the picker
    // before the capability check itself says the feature is live.
    if (openPicker) setPickerOpen(true);
  }, [openPicker]);
  const [directPick, setDirectPick] = useState<{ chain: string; token: string } | null>(null);
  const [swapPick, setSwapPick] = useState<SwapPick | null>(null);
  const swapped = useSwappedSell();
  const swappedBusy = !!SWAPPED_STAGE_LABELS[swapped.stage];
  const [openIntent, setOpenIntent] = useState<SellIntent | null>(null);
  // Whether this server sells any token by swapping it first. Off until the
  // server says so: with RAMP_CONVERSION_SELL unset every non-stable pick
  // would fail at its quote, so the screen stays the stablecoin sale it was.
  const [swappedSellOn, setSwappedSellOn] = useState(false);
  const [cancellingSale, setCancellingSale] = useState(false);

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

  // A deposit that may be out (a lost answer, a kill after the broadcast — the
  // swapped sale's own send lands here too) is waited on, never offered again.
  useEffect(() => {
    setHeld(null);
    if (!order?.id) return;
    let cancelled = false;
    depositMayBeOut(depositHoldKey(order.id), order.id).then((h) => {
      if (!cancelled) setHeld(h);
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
          const picked = directPick
            ? q.venues.find((v) => v.chain === directPick.chain && v.token.toUpperCase() === directPick.token)
            : undefined;
          const preferred = asset
            ? q.venues.find((v) => v.token.toUpperCase() === asset.toUpperCase())
            : undefined;
          return kept ?? picked ?? preferred ?? q.best ?? null;
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
  }, [amount, asset, currencyCode, directPick]);

  // ── The swapped sale ───────────────────────────────────────────────────

  // Her unfinished sale, if any, so "Finish your sale" can pick it up — and
  // whether swapped sells exist at all: a 404 means the routes are not
  // registered, so no swapped-sell UI is shown. Any other failure proves
  // nothing either way and leaves the last answer standing.
  const refreshOpenIntent = useCallback(() => {
    getSwappedSellState()
      .then(({ enabled, intent }) => {
        setSwappedSellOn(enabled);
        setOpenIntent(intent);
      })
      .catch((e) => {
        // A server that answered at all has the routes; only 404 says it doesn't.
        if (e instanceof PaycrestError) setSwappedSellOn(true);
        setOpenIntent(null);
      });
  }, []);
  useEffect(() => {
    if (address) refreshOpenIntent();
  }, [address, refreshOpenIntent]);

  const onPick = useCallback(
    (token: LiFiToken, chainId: number) => {
      if (!address) return;
      const chain = getChainByChainId(chainId)?.key;
      const sym = token.symbol.toUpperCase();
      // A route Paycrest fills directly stays today's sale. Before anything is
      // typed there is no quote to ask, so the stablecoins it sells stand in.
      const liveVenue = chain
        ? quote?.venues.find((v) => v.chain === chain && v.token.toUpperCase() === sym)
        : undefined;
      const direct = quote?.venues.length
        ? !!liveVenue
        : !!chain && DIRECT_TOKENS.has(sym) && chain in CHAIN_LABELS;
      if (chain && direct) {
        setSwapPick(null);
        setDirectPick({ chain, token: sym });
        if (liveVenue) setVenue(liveVenue);
        setPickerOpen(false);
        return;
      }
      // Nothing can sell from Solana, Bitcoin or Sui yet (the server refuses
      // them: nothing can check a route signed there). Said here, at the
      // pick, rather than as a failed quote later.
      if (isNonEVMLiFiChain(chainId)) {
        Alert.alert(
          'Not supported yet',
          `Selling ${token.symbol} from ${networkName(chainId)} isn't supported yet. Pick a coin on another network.`,
        );
        return;
      }
      // An EVM sale is sent from her EVM wallet itself.
      setDirectPick(null);
      setSwapPick({ token: { ...token, chainId }, chainId, sourceAddress: address });
      setPickerOpen(false);
    },
    [address, quote],
  );

  const onSellSwapped = useCallback(
    (amount: string) => {
      if (!swapPick || !address || !bank) return;
      swapped.start({
        sourceChainId: swapPick.chainId,
        token: swapPick.token,
        amount,
        walletAddress: address,
        sourceAddress: swapPick.sourceAddress,
        bank: { code: bank.code, account: accountNo, name: accountName || undefined },
        currency: currencyCode,
      });
    },
    [swapPick, address, bank, accountNo, accountName, currencyCode, swapped],
  );

  // A sale stuck before its payout opened (a lost reply, or a swap hash that
  // someone else recorded first) would otherwise block every new sale.
  const confirmCancelSale = useCallback(() => {
    if (!openIntent) return;
    const intent = openIntent;
    Alert.alert(
      'Cancel this sale?',
      'If your swap still arrives, the USDC will be in your wallet on Base and you can sell it here as USDC.',
      [
        { text: 'Keep it', style: 'cancel' },
        {
          text: 'Cancel sale',
          style: 'destructive',
          onPress: async () => {
            setCancellingSale(true);
            try {
              await swapped.cancel(intent);
            } catch (e) {
              Alert.alert('Could not cancel', e instanceof Error && e.message ? e.message : 'Please try again.');
            } finally {
              setCancellingSale(false);
              refreshOpenIntent();
            }
          },
        },
      ],
    );
  }, [openIntent, swapped, refreshOpenIntent]);

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

  /** The order screen, where an order that must not be sent to (again) is followed.
   *  `unconfirmed`: a send whose outcome is unknown — it says so, and offers no way to send. */
  const showOrder = useCallback((id: string, unconfirmed = false) => {
    router.replace(
      (unconfirmed ? `/wallet/paycrest/order/${id}?unconfirmed=1` : `/wallet/paycrest/order/${id}`) as never,
    );
  }, []);

  // A finished swapped sale lands on the order screen like a direct one. One
  // that stopped after its payout opened drops into this screen's own deposit
  // view, whose Send retries the step that failed.
  useEffect(() => {
    if (swapped.stage === 'done' && swapped.order) {
      const to = swapped.depositTx
        ? `/wallet/paycrest/order/${swapped.order.id}?sentTx=${encodeURIComponent(swapped.depositTx)}`
        : `/wallet/paycrest/order/${swapped.order.id}`;
      router.replace(to as never);
      return;
    }
    if (swapped.stage === 'failed') {
      if (swapped.order?.status === 'awaiting_deposit') setOrder(swapped.order);
      refreshOpenIntent();
    }
  }, [swapped.stage, swapped.order, swapped.depositTx, refreshOpenIntent]);

  /**
   * Send the deposit — once. The order is read first and sent only while it
   * still waits on a deposit nothing says left (not reported, not claimed, no
   * hold on this phone), from the wallet that opened it, for exactly what is on
   * screen. A self-paid send is held under the order right before it is
   * broadcast (services/depositGuard); only a refusal the server answered lifts
   * that, and an unanswered send is followed on the order screen, never offered
   * again.
   */
  const onSend = useCallback(async () => {
    if (!order || !address) return;
    setSending(true);
    setSendError(null);
    const holdKey = depositHoldKey(order.id);
    try {
      let fresh: PaycrestOrder;
      try {
        fresh = await getOrder(order.id);
      } catch (e) {
        setSendError(e instanceof PaycrestError ? e.message : CHECK_FAILED);
        return;
      }
      if (!(await stillToSend(holdKey, fresh))) {
        // Reported, claimed, possibly sent from here, or moved on: the order
        // screen says which. Nothing is sent again.
        if (isTerminal(fresh.status)) await clearHold(holdKey);
        showOrder(fresh.id);
        return;
      }
      if (fresh.walletAddress?.toLowerCase() !== address.toLowerCase()) {
        setOrder(fresh);
        setSendError(OTHER_WALLET);
        return;
      }
      if (!sameDeposit(order, fresh)) {
        // Shown as it is now; a further tap sends what she then sees.
        setOrder(fresh);
        setSendError(ORDER_CHANGED);
        return;
      }
      let txHash: string;
      try {
        txHash = await sendHeldDeposit(fresh, address, nativeBalanceFor(fresh.chain, balances), holdKey);
      } catch (e) {
        if (depositOutcomeUnknown(e)) {
          showOrder(fresh.id, true);
          return;
        }
        // A refusal the server answered: nothing left the wallet. Server and
        // RPC messages here are written to be read — show them.
        setSendError(e instanceof Error ? e.message : 'Could not send. Please try again.');
        return;
      }
      goToOrder(fresh.id, txHash);
    } finally {
      setSending(false);
    }
  }, [order, address, balances, goToOrder, showOrder]);

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

  // Where the naira goes is chosen and checked — the same for either sale.
  const payoutReady =
    !!bank && isValidAccountNumber(currencyCode, accountNo, destKind) && (!requiresNameCheck || !!accountName);

  const ready = !!venue && !!address && payoutReady && !fundingBlocks;

  const sellingLabel = swapPick
    ? `${swapPick.token.symbol} on ${networkName(swapPick.chainId)}`
    : directPick
      ? `${directPick.token} on ${CHAIN_LABELS[directPick.chain] ?? directPick.chain}`
      : undefined;

  // ── Deposit instructions ────────────────────────────────────────────────
  if (order) {
    // No key on this device, or a chain we cannot build a tx for: the address
    // is the only thing we can offer, so it opens expanded.
    const canSendInApp =
      !!address && !!CHAINS[order.chain as ChainKey] && !!order.depositAddress && !!order.tokenAmount;
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
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

          {/* This order's deposit may already have left this phone: nothing
              here may send it again, show where to send it, or cancel the
              order under it. It is followed on the order screen. */}
          {held ? (
            <Card style={{ borderWidth: 1, borderColor: colors.warning, gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
                Your deposit may already be on its way
              </Text>
              <Text variant="caption" style={{ color: colors.textMuted }}>
                {DEPOSIT_OUTCOME_UNKNOWN}
              </Text>
              <Button title="Check on this order" onPress={() => showOrder(order.id, true)} fullWidth />
            </Card>
          ) : (
            <>
              {/* The wallet holding the key is right here — asking the user to
                  copy an address into another app was never the shorter path. */}
              {canSendInApp && (
                <Button
                  title={`Send ${order.tokenAmount} ${order.token}`}
                  onPress={onSend}
                  loading={sending}
                  disabled={expired || sending || held === null}
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
                  claim the order is dead while their deposit is still in flight —
                  and until the hold is known to be clear. */}
              {!order.paymentClaimedAt && held === false && (
                <Button
                  title="Cancel this sale"
                  variant="ghost"
                  onPress={confirmCancel}
                  loading={cancelling}
                  disabled={cancelling}
                  fullWidth
                />
              )}
            </>
          )}
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount, route, payout ───────────────────────────────────────────────

  // Where the naira goes — the same for either kind of sale.
  const payoutFields = (
    <>
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
    </>
  );

  const swappedStatus = <SwappedSellStatus stage={swapped.stage} error={swapped.error} />;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
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

          {/* A TSA Pay payment's sale belongs to its payment: finished or closed from here, it could be
              written off under a swap still being signed there — and that swap pays the merchant. */}
          {openIntent?.payPaymentId && !swappedBusy && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning, gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
                A payment of yours is still going through
              </Text>
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Open its payment link to check on it. You can start a new sale once it has finished.
              </Text>
            </Card>
          )}

          {openIntent && !openIntent.payPaymentId && !swappedBusy && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning, gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
                You have a sale that didn&apos;t finish
              </Text>
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Nothing is lost. Tap below and we&apos;ll carry on from where it stopped.
              </Text>
              <Button
                title="Finish your sale"
                onPress={() => swapped.resume(openIntent, address ?? undefined)}
                fullWidth
              />
              {canCancelSale(openIntent) && (
                <Button
                  title="Cancel this sale"
                  variant="ghost"
                  onPress={confirmCancelSale}
                  loading={cancellingSale}
                  disabled={cancellingSale}
                  fullWidth
                />
              )}
            </Card>
          )}

          {swappedSellOn && (
            <SelectRow
              label="What are you selling?"
              value={sellingLabel}
              placeholder="USDC or USDT"
              onPress={() => setPickerOpen(true)}
            />
          )}

          {swapPick ? (
            <SwappedSellForm
              pick={swapPick}
              walletAddress={address}
              currency={currencyCode}
              symbol={symbol}
              payoutReady={payoutReady}
              busy={swappedBusy}
              payout={payoutFields}
              status={swappedStatus}
              onSell={onSellSwapped}
            />
          ) : (
            <>
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

              {payoutFields}

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

              {/* A sale resumed from "Finish your sale" runs here, with no pick. */}
              {swappedStatus}

              <Button
                title="Continue"
                onPress={onConfirm}
                loading={creating}
                disabled={!ready || quoting || creating || swappedBusy}
                fullWidth
              />
            </>
          )}
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — you send the crypto yourself.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <AssetPicker
        visible={swappedSellOn && pickerOpen}
        side="from"
        holdingsOnly
        title="Choose what to sell"
        allow={notWalletMcgp}
        note="MCGP in your wallet can't be sold for naira. Spendable MCGP sells from the MCGP row."
        onSelect={onPick}
        onClose={() => setPickerOpen(false)}
      />
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
