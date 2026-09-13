// app/wallet/it/sell-stable.tsx
//
// Sell USDT/USDC for Naira (brokered). The user's stablecoin goes from their
// own wallet STRAIGHT to Flutterwave's Polygon address — the platform holds no
// float and never custodies. The backend pays out `received / quoted`, which
// is the single fact that shapes this screen:
//
//   the fiat quote is composed against what will ARRIVE, never against what
//   the user typed.
//
// A bridge delivers less than it was given, so quoting the typed amount would
// make the final figure always shrink — a bait-and-switch. `estimateArrival`
// turns LiFi's estimated destination amount into the number we price.
//
// Flow: form → review → create → authorize → sign → broadcast → report → the
// order screen. This file owns every step up to the hand-off, plus the review
// SNAPSHOT the signing step reads.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { ethers } from 'ethers';
import { useLocalSearchParams, router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  CountdownBar,
  Divider,
  Icon,
  Input,
  PickerSheet,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { BankDestination, type BankDestinationValue } from '@/components/it/BankDestination';
import { DEFAULT_TOKENS, getChain, type ChainKey } from '@/constants/chains';
import { getLiFiQuote, getLiFiToken, getLiFiTokens, type LiFiToken } from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import { parseTokenAmount } from '@/services/p2p';
import { ensureGas, getActiveWallet, readErc20BalanceOf, signTransaction } from '@/services/wallet';
import {
  getDiscoveredTokens,
  getTransactionHistory,
  prepareSendTransaction,
  sendPreparedTransaction,
  submitTransaction,
  type Transaction,
} from '@/services/walletApi';
import { mapSubmitError, UNCONFIRMED_COPY } from '@/services/submitErrors';
import {
  IT_AGREEMENT_REQUIRED,
  IT_STABLE_DEPOSIT_CLAIMED,
  createStableSell,
  makeIdempotencyKey,
  quoteStableSell,
  reportStableDeposit,
  type StableAsset,
  type StableQuote,
} from '@/services/itApi';
import {
  POLYGON_STABLE_DECIMALS,
  STABLE_MAX_DECIMALS,
  STABLE_MIN_SEND,
  belowMinimum,
  estimateArrival,
  exceedsBalance,
  exceedsStablePrecision,
  formatKobo,
  formatSendVsArrive,
  isLikelyTxHash,
  subtractKobo,
  truncateToStableDecimals,
} from '@/utils/stableSellQuote';

// 'claimed' and 'reportFailed' are BOTH post-broadcast: the user's stablecoin
// is already on its way to Flutterwave on either. They are separate screens
// because the recovery differs — one is a support case, the other retries.
type Screen = 'form' | 'review' | 'signing' | 'error' | 'claimed' | 'reportFailed';

/** Everything the send needs, taken from the create response and NOWHERE else. */
type CreatedSell = {
  intentId: string;
  /** Flutterwave's Polygon address. */
  depositAddress: string;
  /** The exact ERC-20 contract ConfirmStableDeposit verifies the arrival
   *  against. Polygon carries both native USDC and bridged USDC.e, so this is
   *  never resolved app-side. */
  depositTokenAddress: string;
  /** The figure the backend reconciles the arrival against ("%.6f"). */
  depositAmount: string;
};

/**
 * Where a broadcast hash lives until the backend has acknowledged it.
 *
 * Keyed by intent so a second sell cannot overwrite the first one's hash, and
 * so the order screen can find it by id alone.
 */
const pendingTxKey = (intentId: string) => `it_stable_pending_tx_${intentId}`;

// Backoff between report attempts. `req` mints a fresh Idempotency-Key per
// call, so the middleware dedups nothing here — what makes the retry safe is
// ReportStableDeposit itself returning early for the same (intent, hash).
const REPORT_BACKOFF_MS = [0, 1000, 3000];

const shortHash = (h: string) => (h.length > 16 ? `${h.slice(0, 10)}…${h.slice(-6)}` : h);

const EMPTY_DEST: BankDestinationValue = {
  bankCode: '',
  bankName: '',
  accountNumber: '',
  accountName: '',
  ready: false,
};

// Flutterwave settles on Polygon; every other source chain is a bridge.
const DEST_CHAIN_ID = 137;

// Delivery more than this far ABOVE the quote is held for manual review rather
// than paid automatically (stableOverDeliveryCeiling, it_stable_sell.go:47-59).
// Surfaced on the review screen because an over-conservative LiFi estimate on a
// volatile route can trip it, and a user whose sell silently enters review with
// no explanation will assume it is lost.
const OVER_DELIVERY_NOTE =
  'If noticeably more than this arrives (over 2%), we hold the payout for a quick manual review instead of paying out automatically.';

// `formatKobo`, `truncateToStableDecimals` and `subtractKobo` live in
// utils/stableSellQuote.ts: they are pure money functions, and this repo can
// only unit-test them there.

// Composing a quote either succeeds with BOTH halves, or fails with a message
// the user can act on. There is deliberately no third outcome: every failure
// path here — no route, unpriceable arrival, a rejected quote — must block the
// review rather than fall back to pricing the amount the user typed.
type QuoteOutcome =
  | { ok: true; arrival: string; quote: StableQuote }
  | { ok: false; message: string };

export default function ITSellStable() {
  const { colors, spacing, radius } = useTheme();

  const { asset: assetParam } = useLocalSearchParams<{ asset: string }>();
  const asset: StableAsset = assetParam === 'USDT' ? 'USDT' : 'USDC';

  const [screen, setScreen] = useState<Screen>('form');

  // --- form inputs ---------------------------------------------------------
  const [chainKey, setChainKey] = useState<ChainKey>('polygon');
  const [showChainPicker, setShowChainPicker] = useState(false);
  const [amount, setAmount] = useState('');
  const [dest, setDest] = useState<BankDestinationValue>(EMPTY_DEST);
  const [sourceToken, setSourceToken] = useState<LiFiToken | null>(null);
  const [tokenLoading, setTokenLoading] = useState(false);
  const [balance, setBalance] = useState<string | null>(null); // null = UNKNOWN, not zero
  // An unreadable balance gets its OWN surface, deliberately not `quoteError`.
  // The read only throws after every RPC times out — many seconds, well past the
  // 500ms debounce and the quote round-trip — so it can land AFTER a successful
  // quote has cleared `quoteError`. Writing it there would leave a valid payout
  // on screen beside a red line, with Review enabled but refusing to open on
  // every press. An unknown balance must SKIP the balance guard, never gate the
  // form; this string is advisory and is rendered under the amount field.
  const [balanceError, setBalanceError] = useState<string | null>(null);
  const [chainBalances, setChainBalances] = useState<Record<string, string>>({});
  // Discovery either ran or it did not. Without this flag a chain missing from
  // the map is indistinguishable from a chain holding zero, and showing "0" for
  // a chain we never checked would talk the user out of a chain they can use.
  const [chainBalancesLoaded, setChainBalancesLoaded] = useState(false);
  const [quote, setQuote] = useState<StableQuote | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [arrival, setArrival] = useState<string | null>(null);
  const chainId = getChain(chainKey).chainId;
  const chainLabel = getChain(chainKey).name; // "BNB Smart Chain", "Polygon", …
  const bridged = chainKey !== 'polygon';

  // The SOURCE token's decimals — the chain the user sends FROM, where BSC
  // stablecoins are genuinely 18. Never POLYGON_STABLE_DECIMALS, which is the
  // DESTINATION's; conflating the two is a 10^12 error. null until the token
  // resolves, which is also what gates the form.
  const sourceDecimals = sourceToken?.decimals ?? null;

  // --- two DISTINCT error surfaces, deliberately ---------------------------
  // quoteError belongs to the amount/chain block and clears on every re-quote;
  // error belongs to the screen and survives until the user acts. Rendering one
  // string for both would let a stale "no route" sit under a signing failure.
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // --- the review SNAPSHOT (Task 7 reads ONLY these) -----------------------
  // Frozen copies of the inputs, so a late quote or an edited field cannot pair
  // with a create that is already in flight.
  const [reviewAmount, setReviewAmount] = useState('');
  const [reviewArrival, setReviewArrival] = useState('');
  // The chain the snapshot was priced on. Task 7 sends this as `sourceChain`,
  // and the review screen labels itself from it — reading live `chainKey` there
  // would let a chain change behind the review screen mislabel the sell.
  const [reviewChainKey, setReviewChainKey] = useState<ChainKey>('polygon');
  const [reviewSourceDecimals, setReviewSourceDecimals] = useState(6);
  const [reviewSourceTokenAddress, setReviewSourceTokenAddress] = useState('');
  const [reviewNetKobo, setReviewNetKobo] = useState<string | null>(null);
  const [reviewGrossKobo, setReviewGrossKobo] = useState<string | null>(null);
  const [reviewAfterSpreadKobo, setReviewAfterSpreadKobo] = useState<string | null>(null);
  const [reviewFeeKobo, setReviewFeeKobo] = useState<string | null>(null);
  const [reviewRateKobo, setReviewRateKobo] = useState<string | null>(null);
  const [reviewExpiresAt, setReviewExpiresAt] = useState('');
  const [reviewBankCode, setReviewBankCode] = useState('');
  const [reviewBankName, setReviewBankName] = useState('');
  const [reviewAccountNumber, setReviewAccountNumber] = useState('');
  const [reviewAccountName, setReviewAccountName] = useState('');
  const [reviewIdempotencyKey, setReviewIdempotencyKey] = useState('');
  // Non-null when the server re-quoted to a different net at create time; drives
  // the "the rate moved" banner below.
  const [repricedTo, setRepricedTo] = useState<string | null>(null);
  // The figure the banner says the payout moved FROM. Held separately because
  // accepting a reprice overwrites reviewNetKobo with the new net (the next
  // create is compared against it), so reviewNetKobo can no longer play the
  // "was" role — it would read "now ₦X (was ₦X)".
  const [repricedFrom, setRepricedFrom] = useState<string | null>(null);

  // --- signing --------------------------------------------------------------
  const [showAuth, setShowAuth] = useState(false);
  const [signingStatus, setSigningStatus] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  // The created intent, held between create and the signature. Nothing about
  // the send is re-derived after this point.
  const [created, setCreated] = useState<CreatedSell | null>(null);
  // Set when a send failed with no hash in hand: we cannot prove nothing was
  // broadcast, so the error screen offers a recovery route rather than a dead
  // end. Holds the intent id the recovery reports against.
  const [recoveryIntentId, setRecoveryIntentId] = useState<string | null>(null);
  // The broadcast hash, held for display on every post-broadcast screen. This
  // is a COPY for the UI — the authoritative one is in AsyncStorage, written
  // before the first report attempt.
  const [broadcastHash, setBroadcastHash] = useState<string | null>(null);
  const [recoverySends, setRecoverySends] = useState<Transaction[] | null>(null);
  const [recoveryLoading, setRecoveryLoading] = useState(false);
  // A history lookup that FAILED, kept apart from one that returned nothing.
  // "We couldn't check" and "you sent nothing" are different sentences, and on
  // this screen the second one is what produces a second send.
  const [recoveryError, setRecoveryError] = useState<string | null>(null);
  // The hash a stranded user pastes in themselves. The only recovery a bridged
  // sell has: getTransactionHistory only sees the DESTINATION chain.
  const [manualHash, setManualHash] = useState('');
  const [manualHashError, setManualHashError] = useState<string | null>(null);
  const [reporting, setReporting] = useState(false);
  // The active EVM address, resolved the way all-swap.tsx does — there is no
  // useWalletAddress hook in this repo. Everything downstream (the balance
  // read, the LiFi quote, the signature pin) is against THIS address.
  const [walletAddress, setWalletAddress] = useState('');
  useEffect(() => {
    getActiveWallet().then((addr) => {
      if (addr) setWalletAddress(addr);
    });
  }, []);

  // `dest` comes back from a controlled child; setState setters are already
  // referentially stable, so this satisfies BankDestination's memoised-onChange
  // requirement without a useCallback wrapper.

  // Chains this asset exists on, straight from the token registry rather than a
  // literal list, so adding a chain there adds it here.
  const chainOptions = useMemo<ChainKey[]>(() => DEFAULT_TOKENS[asset]?.chains ?? ['polygon'], [asset]);

  // Per-chain holdings for the picker, so a chain the user holds nothing on
  // reads as empty rather than mysteriously unquotable. Display only — the
  // authoritative figure for the SELECTED chain is read from the chain below.
  useEffect(() => {
    if (!walletAddress) return;
    let cancelled = false;
    (async () => {
      const res = await getDiscoveredTokens(chainOptions, walletAddress);
      if (cancelled || !res.success || !res.data?.tokens) return;
      // providerAvailable is false when the backend has no indexer key at all —
      // then every chain is genuinely unknown, not empty.
      const complete = res.data.providerAvailable !== false;
      const next: Record<string, string> = {};
      for (const t of res.data.tokens) {
        if (t.symbol?.toUpperCase() !== asset) continue;
        const key = t.chain?.toLowerCase();
        if (!key) continue;
        try {
          next[key] = ethers.formatUnits(t.balance || '0', t.decimals);
        } catch {
          /* a malformed row must not blank the whole picker */
        }
      }
      setChainBalances(next);
      setChainBalancesLoaded(complete);
    })();
    return () => {
      cancelled = true;
    };
  }, [walletAddress, asset, chainOptions]);

  // Resolve the source token on the selected chain, then read the chain's own
  // balance for it. The backend balance endpoint zeroes imported tokens, which
  // is why the selected chain's figure is read directly (all-swap.tsx does the
  // same).
  useEffect(() => {
    if (!walletAddress) return;
    let cancelled = false;
    setTokenLoading(true);
    setSourceToken(null);
    setBalance(null);
    setBalanceError(null);
    (async () => {
      // The spot-trade.tsx resolver: the single-token lookup accepts a SYMBOL
      // and returns a tiny payload; the full per-chain list is multi-MB and
      // flaky on mobile, so it is only the fallback. BOTH halves can miss, and
      // `sourceToken.decimals` on a null token is a TypeError that reads to the
      // user as a broken screen — so resolve into state and gate the form on it.
      const resolveToken = async (id: number, sym: string): Promise<LiFiToken | null> => {
        try {
          const t = await getLiFiToken(id, sym);
          if (t) return t;
        } catch {
          /* fall through to the list */
        }
        try {
          const tokens = await getLiFiTokens(id);
          return tokens.find((t) => t.symbol?.toUpperCase() === sym) ?? null;
        } catch {
          return null;
        }
      };

      const token = await resolveToken(chainId, asset);
      if (cancelled) return;
      if (!token) {
        setTokenLoading(false);
        setSourceToken(null);
        setQuoteError(`Couldn't load ${asset} on ${chainLabel}. Check your connection and try again.`);
        return;
      }
      setSourceToken(token);
      setTokenLoading(false);

      // readErc20BalanceOf THROWS when every configured RPC fails — deliberately,
      // so "unknown" stays distinguishable from "zero". Treating unknown as zero
      // would block the form with no explanation, so keep it null and say so.
      try {
        const raw = await readErc20BalanceOf(token.address, walletAddress, chainKey);
        if (cancelled) return;
        setBalance(ethers.formatUnits(raw, token.decimals));
        setBalanceError(null);
      } catch {
        if (cancelled) return;
        setBalance(null);
        // NOT setQuoteError: this must not block a quote that already succeeded.
        setBalanceError(`Couldn't read your ${asset} balance on ${chainLabel}. Sending is still fine — the chain checks it.`);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [walletAddress, asset, chainKey, chainId, chainLabel]);

  // Compose the fiat quote against what will ARRIVE.
  //
  // Returns an outcome rather than writing state, so both callers — the
  // debounced form effect and the countdown's onExpire refresh — get the same
  // guards and the same failure messages without one of them racing the other's
  // setState.
  const composeQuote = useCallback(async (): Promise<QuoteOutcome> => {
    // DEST_CHAIN_ID is a literal, but `getChain('polygon')` is network-aware —
    // it resolves to Amoy (80002) on a testnet build. Rather than silently
    // pricing a route to mainnet 137 and preparing a same-chain send on it,
    // fail here, before anything is quoted. Mainnet-only today; this is the
    // guard that keeps it that way loudly.
    if (getChain('polygon').chainId !== DEST_CHAIN_ID) {
      return { ok: false, message: 'Selling stablecoins is not available on this network.' };
    }
    if (exceedsStablePrecision(amount)) {
      // A guaranteed 400 from StableToBaseUnits — say why rather than letting
      // it come back as a generic failure after a round trip.
      return { ok: false, message: `${asset} amounts can have at most 6 decimal places` };
    }
    if (belowMinimum(asset, amount)) {
      return { ok: false, message: `Minimum sell is ${STABLE_MIN_SEND[asset]} ${asset}` };
    }
    if (!walletAddress) {
      return { ok: false, message: 'Still loading your wallet — try again in a moment' };
    }
    if (!sourceToken || sourceDecimals === null) {
      return {
        ok: false,
        message: `Couldn't load ${asset} on ${chainLabel}. Check your connection and try again.`,
      };
    }

    // TWO different decimals below, and conflating them is a 10^12 error.
    // `sourceDecimals` is the token on the chain the user sends FROM (BSC
    // stablecoins are 18); POLYGON_STABLE_DECIMALS is the destination, where
    // LiFi's toAmount is always denominated because toChain is pinned to 137.
    //
    // Decimals come from the resolved LiFi token rather than p2pTokenDecimals:
    // that helper is a Sonic/BSC whitelist and silently returns 6 for Arbitrum,
    // Base, Linea and Avalanche — correct today, but by accident.
    let fromAmountBase: string;
    try {
      fromAmountBase = parseTokenAmount(amount, sourceDecimals);
    } catch {
      // Shape is already enforced by belowMinimum's grammar; this is the belt
      // to that braces.
      return { ok: false, message: 'Enter a plain decimal amount' };
    }

    let lifiToAmount: string | null = null;
    if (bridged) {
      try {
        // getLiFiQuote THROWS on a non-2xx — a no-route response must surface
        // as a message, not crash the effect.
        const routed = await getLiFiQuote({
          fromChain: chainId,
          toChain: DEST_CHAIN_ID,
          fromToken: sourceToken.address,
          // PRICING ONLY. `toToken` is passed through verbatim to LiFi, which
          // accepts a symbol as well as an address. Using the SYMBOL here is
          // deliberate: the app must never pick a Polygon USDC contract itself,
          // because Polygon carries both native USDC and bridged USDC.e and the
          // backend confirms against exactly one of them. The authoritative
          // contract arrives with the create response and is used for the
          // EXECUTION quote and the transfer in Task 7. A symbol that resolves
          // to the other variant can therefore only skew this ESTIMATE — it can
          // never misroute funds.
          toToken: asset,
          fromAmount: fromAmountBase,
          fromAddress: walletAddress,
          // The real deposit address only exists after create (Task 7). LiFi
          // just needs a well-formed destination to PRICE a route, so the
          // user's own address stands in; Task 7's execution quote uses the
          // real one. Looks wrong without this comment; it is deliberate.
          toAddress: walletAddress,
        });
        lifiToAmount = routed.toAmount;
      } catch {
        return { ok: false, message: `No route from ${chainLabel} — try another chain` };
      }
    }

    let arrivalMajor: string;
    try {
      // POLYGON_STABLE_DECIMALS, not sourceDecimals: LiFi's toAmount is
      // denominated on the DESTINATION chain.
      arrivalMajor = estimateArrival(amount, lifiToAmount, POLYGON_STABLE_DECIMALS);
    } catch {
      // LiFi answered but gave us nothing usable. Block rather than quote the
      // typed amount — see estimateArrival's contract.
      return { ok: false, message: 'Could not price this route — try another chain' };
    }

    // The floor is Flutterwave's OUTBOUND minimum, so it applies to what
    // ARRIVES, not to what is sent. Checking the typed amount alone lets a
    // bridged 10.00 pass here, deliver 9.88, and be rejected for conversion
    // with the funds already at Flutterwave and nothing app-side able to undo
    // it. Same rule as the fiat quote: price and gate against the arrival.
    if (belowMinimum(asset, arrivalMajor)) {
      return {
        ok: false,
        message: `After bridge fees less than ${STABLE_MIN_SEND[asset]} ${asset} would arrive — send a little more`,
      };
    }

    const q = await quoteStableSell({ asset, amount: arrivalMajor, currency: 'NGN' });
    if (!q.success || !q.data) {
      return { ok: false, message: q.message || 'Could not price this amount — try again shortly' };
    }
    return { ok: true, arrival: arrivalMajor, quote: q.data };
  }, [amount, asset, bridged, chainId, chainLabel, sourceDecimals, sourceToken, walletAddress]);

  // Debounced re-quote. The quote is dropped the instant an input changes, so
  // a stale figure can never be the one the user reviews.
  useEffect(() => {
    setQuote(null);
    setArrival(null);
    if (!amount.trim()) {
      setQuoting(false);
      setQuoteError(null);
      return;
    }
    let cancelled = false;
    setQuoting(true);
    const timer = setTimeout(async () => {
      const outcome = await composeQuote();
      if (cancelled) return;
      setQuoting(false);
      if (!outcome.ok) {
        setQuoteError(outcome.message);
        return;
      }
      setQuoteError(null);
      setArrival(outcome.arrival);
      setQuote(outcome.quote);
    }, 500);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [amount, composeQuote]);

  // The snapshot Task 7 creates the intent from. NOTHING in Task 7 reads live
  // form state: that is the entire point — create must not pair a stale quote
  // with edited inputs.
  //
  // Takes the arrival/quote pair explicitly rather than reading state, so the
  // countdown's refresh path can snapshot a freshly-composed quote in the same
  // tick instead of waiting a render for setState to land.
  const enterReview = useCallback(
    (arrivalMajor: string, q: StableQuote, token: LiFiToken, destination: BankDestinationValue) => {
      setReviewAmount(amount); // typed, major units
      setReviewArrival(arrivalMajor); // what we expect to LAND
      setReviewChainKey(chainKey);
      // Both halves of the source token are part of the snapshot: Task 7 parses
      // reviewAmount with these decimals and builds the execution quote's
      // `fromToken` from this address. One name for one value — `sourceToken` is
      // live form state and must not be read after this point.
      setReviewSourceDecimals(token.decimals);
      setReviewSourceTokenAddress(token.address);
      setReviewNetKobo(q.net); // the payout figure being shown
      setReviewGrossKobo(q.gross);
      setReviewAfterSpreadKobo(q.afterSpread);
      setReviewFeeKobo(q.fee);
      setReviewRateKobo(q.rate);
      setReviewExpiresAt(q.expiresAt);
      setReviewBankCode(destination.bankCode.trim());
      setReviewBankName(destination.bankName.trim());
      setReviewAccountNumber(destination.accountNumber.trim());
      setReviewAccountName(destination.accountName.trim());
      // One key per confirmed snapshot. Minted HERE, not at mount: the
      // middleware 409s a key replayed with a DIFFERENT payload, so a
      // mount-scoped key would wedge a user who backs out, edits the amount and
      // confirms again.
      setReviewIdempotencyKey(makeIdempotencyKey());
      setRepricedTo(null); // clear any prior reprice banner
      setRepricedFrom(null);
      // A fresh snapshot invalidates any intent created from the previous one.
      setCreated(null);
      setError(null);
      setScreen('review');
    },
    [amount, chainKey]
  );

  const goReview = () => {
    setError(null);
    if (exceedsStablePrecision(amount)) {
      setQuoteError(`${asset} amounts can have at most 6 decimal places`);
      return;
    }
    if (belowMinimum(asset, amount)) {
      setQuoteError(`Minimum sell is ${STABLE_MIN_SEND[asset]} ${asset}`);
      return;
    }
    // An UNKNOWN balance skips this comparison rather than failing it: the
    // chain rejects an over-send anyway, and a transient RPC blip must not
    // become an unexplained locked form. exceedsBalance compares as strings —
    // an 18-decimal balance through a float can disagree at the last digit,
    // which on a Max send is exactly where it matters.
    if (balance !== null && exceedsBalance(amount, balance)) {
      setQuoteError(`More than your ${asset} balance on ${chainLabel}`);
      return;
    }
    if (!sourceToken) {
      setQuoteError(`Couldn't load ${asset} on ${chainLabel}. Check your connection and try again.`);
      return;
    }
    if (quoteError || !quote || !arrival) {
      setQuoteError(quoteError || 'Waiting for a price — try again in a moment');
      return;
    }
    if (!dest.ready) {
      setError('Select a bank and enter a valid account number');
      return;
    }
    enterReview(arrival, quote, sourceToken, dest);
  };

  // The quote expired while the user sat on the review screen. Re-price and
  // re-enter review through the same snapshot path, so the new figure is SHOWN
  // (with a fresh key) rather than swapped in silently.
  const refreshExpiredQuote = useCallback(async () => {
    // NOT while a create is in flight or the PIN modal is open. The countdown
    // keeps ticking through both (the modal renders inside this branch, so
    // `screen` is still 'review'), and a refresh that resolves after the create
    // would overwrite the review card with a figure the created intent was
    // NOT priced at — the user authorizes against N2 on screen and is paid N1.
    // Skipping is strictly safer: the server re-quotes at create time anyway,
    // and the reprice comparison in confirmSell is what protects the user.
    if (creating || showAuth) return;
    if (!sourceToken) {
      setQuoteError(`Couldn't load ${asset} on ${chainLabel}. Check your connection and try again.`);
      setScreen('form');
      return;
    }
    setQuoting(true);
    const outcome = await composeQuote();
    setQuoting(false);
    if (!outcome.ok) {
      setQuoteError(outcome.message);
      setScreen('form');
      return;
    }
    setQuoteError(null);
    setArrival(outcome.arrival);
    setQuote(outcome.quote);
    enterReview(outcome.arrival, outcome.quote, sourceToken, dest);
  }, [asset, chainLabel, composeQuote, creating, dest, enterReview, showAuth, sourceToken]);

  // ── create ───────────────────────────────────────────────────────────────
  //
  // Create MUST come first and MUST come before the PIN prompt: the deposit
  // address AND the deposit token contract both come out of this response, and
  // the app may hardcode neither.
  const confirmSell = useCallback(async () => {
    if (creating) return;
    setError(null);
    setCreated(null); // any intent from a previous confirm is superseded
    setCreating(true);
    try {
      // One key per CONFIRMED REVIEW SNAPSHOT, minted in enterReview(). A
      // network retry of this create must not mint a second intent — every
      // intent is a claim on a deposit — but the key must also change when the
      // order changes, or the middleware 409s the replay.
      //
      // reviewBank*/reviewChainKey, never the live fields: the snapshot exists
      // precisely so create cannot pair a stale quote with edited inputs.
      const res = await createStableSell({
        asset,
        amount: reviewArrival,
        currency: 'NGN',
        sourceChain: reviewChainKey.toUpperCase(),
        bank: { code: reviewBankCode, number: reviewAccountNumber, name: reviewAccountName },
        idempotencyKey: reviewIdempotencyKey,
      });

      // PostStableSell is gated on the trading agreement (it_handler.go:741)
      // but PostStableQuote is not, so the whole form and the quote work right
      // up to here. Generic handling would return the user to the form with
      // "accept the trading agreement" and no way to do so — the hub owns that
      // gate, so send them there, exactly as sell.tsx and buy.tsx do.
      if (res.code === IT_AGREEMENT_REQUIRED) {
        router.replace('/wallet/it');
        return;
      }

      // Guard BEFORE the first dereference. A failed create is
      // {success:false, data: undefined}, so reading res.data.intent would be a
      // TypeError on a money screen. Nothing has been broadcast yet, so failing
      // back to the form here is genuinely safe.
      if (
        !res.success ||
        !res.data?.intent?.id ||
        !res.data?.deposit?.address ||
        !res.data?.deposit?.tokenAddress ||
        !res.data?.deposit?.amount
      ) {
        setError(res.message || 'Could not start this sell');
        setScreen('form');
        return;
      }

      // The server RE-QUOTES on create (PostStableSell calls QuoteStableSell
      // again), so the intent is priced against a possibly-moved rate — not the
      // one the user is looking at. Authorizing against a stale figure is the
      // one place money is actually committed, so compare BEFORE the PIN.
      //
      // Both sides must be present for the comparison to mean anything:
      // netNgnKobo is optional on ITIntent and reviewNetKobo is only set by
      // enterReview(). A missing figure would silently invert `a !== b` into
      // "always reprice" or "never reprice", and neither silent mode is
      // acceptable here — so treat it as an error rather than as agreement.
      const createdNet = res.data.intent.netNgnKobo;
      if (!createdNet || !reviewNetKobo) {
        setError('Could not confirm your payout figure. Start the sell again.');
        setScreen('form');
        return;
      }
      if (createdNet !== reviewNetKobo) {
        // Re-present rather than proceed. Re-minting the key is MANDATORY: the
        // middleware caches 2xx per key, so confirming again with the same key
        // would replay the original create verbatim — same intent, same net —
        // and this comparison would fail identically forever, wedging the user
        // on the confirm button.
        setRepricedFrom(reviewNetKobo);
        setRepricedTo(createdNet);
        setReviewNetKobo(createdNet);
        setReviewIdempotencyKey(makeIdempotencyKey());
        setScreen('review');
        return;
      }

      setCreated({
        intentId: res.data.intent.id,
        depositAddress: res.data.deposit.address,
        depositTokenAddress: res.data.deposit.tokenAddress,
        depositAmount: res.data.deposit.amount,
      });
      setShowAuth(true);
    } finally {
      setCreating(false);
    }
  }, [
    asset,
    creating,
    reviewAccountName,
    reviewAccountNumber,
    reviewArrival,
    reviewBankCode,
    reviewChainKey,
    reviewIdempotencyKey,
    reviewNetKobo,
  ]);

  // ── report ───────────────────────────────────────────────────────────────

  /**
   * Report the deposit, retrying transient failures.
   *
   * What makes this safe is the SERVER, not the middleware: `req` mints a fresh
   * Idempotency-Key per call, so the middleware sees three unrelated requests.
   * ReportStableDeposit is idempotent for the same (intent, hash) — an already
   * reported hash returns early. Passing a fixed key here to "make it
   * idempotent" would cache a transient 5xx's absence and buy nothing.
   */
  const reportWithRetry = useCallback(
    async (intentId: string, txHash: string): Promise<{ ok: boolean; claimed: boolean; message?: string }> => {
      let lastMessage: string | undefined;
      for (const delay of REPORT_BACKOFF_MS) {
        if (delay > 0) await new Promise((resolve) => setTimeout(resolve, delay));
        const res = await reportStableDeposit(intentId, txHash);
        if (res.success) return { ok: true, claimed: false };
        // Someone else's intent already owns this hash. Retrying can only ever
        // fail the same way, and it is not a generic error.
        if (res.code === IT_STABLE_DEPOSIT_CLAIMED) return { ok: false, claimed: true, message: res.message };
        lastMessage = res.message;
        // A 4xx is the server's considered answer, not a blip — replaying it
        // twice more only delays the recovery screen the user needs. 408 and
        // 429 are the exceptions: both explicitly mean "try again".
        // (`status` is absent when the request never reached the server, which
        // is precisely the case worth retrying.)
        if (res.status && res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429) {
          break;
        }
      }
      return { ok: false, claimed: false, message: lastMessage };
    },
    []
  );

  /**
   * Persist the hash, THEN report it, THEN hand off to the order screen.
   *
   * The one genuinely dangerous failure in this flow is: broadcast succeeded,
   * reporting failed. The funds are in flight to Flutterwave and the backend
   * does not know which intent they belong to. So the hash goes to storage
   * before the network call is attempted, and is never discarded on any path
   * out of here — not on a failed report, not on a claimed deposit.
   *
   * The window between the broadcast returning and the write landing is not
   * closed, and no app-side write can close it. It is small; the recovery
   * screen and the backend's own /wallet/submit-tx record are the fallback when
   * it loses.
   */
  const persistAndReport = useCallback(
    async (intentId: string, txHash: string) => {
      setBroadcastHash(txHash);
      try {
        await AsyncStorage.setItem(pendingTxKey(intentId), txHash);
      } catch (e) {
        // A storage failure must not swallow the hash: reporting is still the
        // fastest route to the backend, and every screen below shows it.
        console.error('[it-sell] could not persist pending deposit hash', e);
      }

      setSigningStatus('Confirming your deposit…');
      const outcome = await reportWithRetry(intentId, txHash);

      // Clear the stored hash on both TERMINAL outcomes — reported, and
      // claimed. Only a report that might still succeed later is worth keeping,
      // and a claimed hash never will: Task 8 retries any stored hash on a
      // non-terminal intent, so leaving it would retry, forever, a call that can
      // only return `claimed` again. The hash stays on screen either way.
      if (outcome.ok || outcome.claimed) {
        try {
          await AsyncStorage.removeItem(pendingTxKey(intentId));
        } catch {
          // A stale key is harmless on the success path: re-reporting the same
          // (intent, hash) returns early server-side.
        }
      }

      if (outcome.ok) {
        router.replace(`/wallet/it/intent/${intentId}`);
        return;
      }

      if (outcome.claimed) {
        setError('This transaction has already been reported. Contact support.');
        setScreen('claimed');
        return;
      }
      setError(outcome.message || 'We could not tell our servers about your transfer.');
      setScreen('reportFailed');
    },
    [reportWithRetry]
  );

  // ── sign + broadcast ─────────────────────────────────────────────────────

  // ensureGas fires its own Alert.alert when onAlert is omitted; with a handler
  // the message lands in this screen's single error surface instead of a
  // competing system dialog. Callers must NOT also call setError.
  const alertFn = useCallback((title: string, message: string) => {
    setError(message ? `${title}. ${message}` : title);
  }, []);

  // signLiFiTransaction always signs with the ACTIVE wallet and takes no
  // expected address, so a wallet switch mid-flow would sign with the wrong key
  // and send someone else's funds. We quoted and created against ONE address —
  // refuse rather than sign.
  //
  // A function, not an inline block, because the LiFi path signs TWICE with a
  // receipt wait between them that can run for minutes: it is re-asserted
  // immediately before executeLiFiSwap. The Polygon path is unaffected —
  // signTransaction takes walletAddress explicitly — but is pinned anyway.
  const assertActiveWallet = useCallback(async () => {
    const active = await getActiveWallet();
    if (!active || !walletAddress || active.toLowerCase() !== walletAddress.toLowerCase()) {
      setError('Your active wallet changed. Start the sell again.');
      return false;
    }
    return true;
  }, [walletAddress]);

  const runSigning = useCallback(async () => {
    setShowAuth(false);
    const sell = created;
    if (!sell) {
      // Nothing to sign against — the create was superseded or never landed.
      setError('That sell is no longer ready. Confirm again to restart it.');
      setScreen('review');
      return;
    }
    setError(null);
    setRecoveryIntentId(null);
    setBroadcastHash(null);
    setRecoverySends(null);
    setSigningStatus('Checking your wallet…');
    setScreen('signing');

    // EVERYTHING below is inside a try/catch, because the 'signing' screen is a
    // bare spinner with no error and no Back button, and AuthorizationModal
    // invokes onAuthorized fire-and-forget (it is typed `() => void`). An
    // unhandled rejection would therefore leave the user on an infinite
    // "Sending 100 USDC…" spinner, escapable only by force-quitting.
    //
    // The throwers are real: signTransaction rejects on "No private key stored
    // for wallet X", an address/key mismatch or a keychain denial, and
    // sendPreparedTransaction does NOT catch its own `sign` callback;
    // getActiveWallet can reject the same way.
    let reachedSend = false;
    try {
      // ADVISORY, not a gate: ensureGas returns TRUE when the RPC read fails, so
      // an RPC blip does not block a send the chain would have accepted. It
      // catches the common "wallet holds zero native token" case and nothing
      // else — a swap can still die for gas after an approval has already cost
      // the user a transaction. Back to review, where funding the wallet and
      // confirming again replays the same create (same key → same intent).
      const gasOk = await ensureGas(reviewChainKey, walletAddress, alertFn);
      if (!gasOk) {
        setScreen('review'); // alertFn already wrote the message
        return;
      }

      if (!(await assertActiveWallet())) {
        setScreen('error');
        return;
      }

      let txHash: string;
      if (reviewChainKey === 'polygon') {
        // No bridge: a plain ERC-20 transfer to Flutterwave's address. The
        // backend's same-chain branch skips the LiFi status lookup entirely.
        //
        // tokenAddress and amount come from the create response, never from a
        // symbol lookup — Polygon carries both native USDC and bridged USDC.e and
        // the backend confirms against exactly one of them.

        // The balance, the Max button and the over-balance guard all read the
        // LiFi-resolved source token; the transfer sends the backend's. On
        // Polygon those are the same chain, so they must be the same contract —
        // native USDC and bridged USDC.e both live here. If they disagree the
        // user has been shown a balance for a token they are not about to send,
        // so stop before any gas is spent rather than hope they agree.
        if (
          reviewSourceTokenAddress &&
          reviewSourceTokenAddress.toLowerCase() !== sell.depositTokenAddress.toLowerCase()
        ) {
          setError(
            `This order expects a different ${asset} contract than the one your balance was read from. Do not send — contact support.`
          );
          setScreen('error');
          return;
        }

        setSigningStatus(`Sending ${sell.depositAmount} ${asset}…`);
        // From here a throw can no longer prove nothing was broadcast.
        reachedSend = true;
        const sent = await sendPreparedTransaction({
          prepare: () =>
            prepareSendTransaction(asset, sell.depositAddress, sell.depositAmount, DEST_CHAIN_ID, {
              tokenAddress: sell.depositTokenAddress,
              decimals: POLYGON_STABLE_DECIMALS,
              fromAddress: walletAddress,
            }),
          sign: (tx) =>
            signTransaction(
              {
                type: 0, // legacy transaction format, as in wallet/send.tsx
                to: tx.to,
                data: tx.data,
                value: tx.value,
                gasLimit: tx.gasLimit,
                gasPrice: tx.gasPrice,
                nonce: tx.nonce,
                chainId: tx.chainId,
              },
              walletAddress
            ),
          submit: (signed) =>
            submitTransaction(
              signed,
              'send',
              asset,
              sell.depositAddress,
              sell.depositAmount,
              DEST_CHAIN_ID,
              walletAddress
            ),
        });
        // sendPreparedTransaction resolves ApiResponse<{txHash}>, NOT a string:
        // assigning it straight through would persist and report "[object
        // Object]" and lose the real hash on the primary route.
        if (!sent.success || !sent.data?.txHash) {
          // We have NO HASH TO PERSIST — which is not the same as "nothing was
          // broadcast", and must not be treated as if it were. submitTransaction
          // returns {success:false} on any network error, including a timeout
          // reading the response after the backend already broadcast the raw
          // transaction; it also returns the result as-is when the backend
          // succeeded but no hash could be extracted. Both land here, so this
          // branch is reachable with the user's funds already in flight.
          // UNCONFIRMED_COPY, not TRANSFER_COPY: per the comment above, we may
          // be here with the transfer already on its way. Telling this user to
          // try again is how one sale becomes two.
          setError(mapSubmitError(sent.message, UNCONFIRMED_COPY));
          setRecoveryIntentId(sell.intentId);
          setScreen('error');
          return;
        }
        txHash = sent.data.txHash;
      } else {
        // Cross-chain. Only NOW is the destination contract known, which is why
        // the EXECUTION quote is built here rather than reusing the pricing quote
        // from the form: both the toAddress and the toToken are the backend's own
        // values.
        setSigningStatus('Building your route…');
        let fromAmountBase: string;
        try {
          fromAmountBase = parseTokenAmount(reviewAmount, reviewSourceDecimals);
        } catch {
          setError('Could not read the amount for this route. Start the sell again.');
          setScreen('error');
          return;
        }
        let execQuote;
        try {
          execQuote = await getLiFiQuote({
            fromChain: getChain(reviewChainKey).chainId,
            toChain: DEST_CHAIN_ID,
            fromToken: reviewSourceTokenAddress,
            toToken: sell.depositTokenAddress,
            fromAmount: fromAmountBase,
            fromAddress: walletAddress,
            toAddress: sell.depositAddress,
          });
        } catch {
          // Nothing signed, nothing broadcast — this one really is a clean fail.
          setError(`No route from ${getChain(reviewChainKey).name} right now — try again shortly.`);
          setScreen('error');
          return;
        }

        // Re-assert immediately before the two signatures.
        if (!(await assertActiveWallet())) {
          setScreen('error');
          return;
        }

        reachedSend = true;
        try {
          txHash = await executeLiFiSwap({
            quote: execQuote,
            fromSymbol: asset,
            fromDecimals: reviewSourceDecimals,
            fromAmount: reviewAmount,
            walletAddress,
            fromChainId: getChain(reviewChainKey).chainId,
            // The brokered ramp charges no platform fee, so this swap owes
            // nothing — say so explicitly, or the server records a debt the
            // sweeper would later collect on a flow that is meant to be free.
            feeFeature: 'none',
            onStage: setSigningStatus,
          });
        } catch (e: any) {
          // executeLiFiSwap throws with a user-facing message. The swap submit
          // can throw on a network error AFTER the backend broadcast, so this is
          // treated as recoverable rather than as proof nothing moved.
          setError(e?.message || `Could not send your ${asset}.`);
          setRecoveryIntentId(sell.intentId);
          setScreen('error');
          return;
        }
      }

      await persistAndReport(sell.intentId, txHash);
    } catch (e: any) {
      console.error('[it-sell] signing failed', e);
      setError(e?.message || 'Something went wrong while sending. Check your wallet, then try again.');
      // Past the submit call we cannot prove nothing was broadcast, so keep the
      // recovery route open rather than presenting a clean failure.
      if (reachedSend) setRecoveryIntentId(sell.intentId);
      setScreen('error');
    }
  }, [
    alertFn,
    assertActiveWallet,
    asset,
    created,
    persistAndReport,
    reviewAmount,
    reviewChainKey,
    reviewSourceDecimals,
    reviewSourceTokenAddress,
    walletAddress,
  ]);

  // ── recovery ─────────────────────────────────────────────────────────────

  // The backend records every broadcast at /wallet/submit-tx, so a send that
  // failed on the RESPONSE is still visible in the user's history. Only
  // transfers to THIS intent's deposit address are offered: reporting any other
  // hash would attach an unrelated transfer to the sell.
  const findMyTransaction = useCallback(async () => {
    if (!created) return;
    setRecoveryLoading(true);
    setRecoveryError(null);
    const res = await getTransactionHistory(1, 10, DEST_CHAIN_ID);
    setRecoveryLoading(false);
    // A failed call is NOT an empty history. Rendering "no transfer found" for
    // a network error tells someone whose money did move that it did not —
    // which is the exact input that produces a second send. Leave
    // recoverySends null so the button stays offered and the lookup is
    // retryable.
    if (!res.success) {
      setRecoveryError(res.message || "Couldn't check your history — try again.");
      return;
    }
    const to = created.depositAddress.toLowerCase();
    setRecoverySends(
      (res.data?.transactions ?? []).filter((t) => t.txHash && t.toAddress?.toLowerCase() === to)
    );
  }, [created]);

  const reportHash = useCallback(
    async (intentId: string, txHash: string) => {
      if (reporting) return;
      setReporting(true);
      setError(null);
      setScreen('signing');
      try {
        await persistAndReport(intentId, txHash);
      } finally {
        setReporting(false);
      }
    },
    [persistAndReport, reporting]
  );

  // Report a hash the user pasted in. The bridged path has no history lookup
  // (getTransactionHistory only sees Polygon), so without this a bridged sell
  // whose broadcast response was lost has nowhere in the app to be recovered —
  // while the error screen tells the user to report it against the order.
  const reportManualHash = useCallback(
    (intentId: string) => {
      const hash = manualHash.trim();
      if (!isLikelyTxHash(hash)) {
        setManualHashError('That does not look like a transaction hash (0x…, 66 characters).');
        return;
      }
      setManualHashError(null);
      // reportStableDeposit is idempotent per (intent, hash), so re-reporting
      // an already-known hash is safe.
      reportHash(intentId, hash);
    },
    [manualHash, reportHash]
  );

  const chainItems: PickerItem[] = chainOptions.map((key) => {
    const held = chainBalances[key] ?? (chainBalancesLoaded ? '0' : undefined);
    return {
      id: key,
      label: getChain(key).name,
      sublabel:
        held === undefined
          ? 'Balance unknown'
          : `${Number(held).toLocaleString(undefined, { maximumFractionDigits: 6 })} ${asset}`,
      icon: 'wallet' as const,
    };
  });

  const sendVsArrive = formatSendVsArrive(reviewAmount, reviewArrival);
  const spreadKobo = subtractKobo(reviewGrossKobo, reviewAfterSpreadKobo);
  // Review reads the SNAPSHOT's chain, never the live picker's.
  const reviewChainLabel = getChain(reviewChainKey).name;
  const reviewBridged = reviewChainKey !== 'polygon';
  const formSendVsArrive = arrival ? formatSendVsArrive(amount, arrival) : null;

  if (screen === 'signing') {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}
      >
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>
          {signingStatus ?? 'Working…'}
        </Text>
      </SafeAreaView>
    );
  }

  // Someone else's intent already owns this hash. This is the front-running
  // case surfacing — a real adversarial situation the backend cannot fully
  // prevent — so it gets its own screen rather than reading as a generic
  // failure. The stored hash is deliberately NOT cleared.
  if (screen === 'claimed') {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}
      >
        <Icon name="warning" size={64} color={colors.warning} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary, textAlign: 'center' }}>
          This transaction has already been reported
        </Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          Your {asset} was sent, but another order has already claimed this transaction. Contact
          support with the reference below — do not send again.
        </Text>
        {broadcastHash ? (
          <Text variant="caption" numeric style={{ marginTop: spacing.lg, color: colors.textPrimary, textAlign: 'center' }}>
            {broadcastHash}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        {created ? (
          <Button
            title="Open this order"
            onPress={() => router.replace(`/wallet/it/intent/${created.intentId}`)}
          />
        ) : null}
      </SafeAreaView>
    );
  }

  // Broadcast succeeded, reporting did not. The hash is in storage and on
  // screen; it is never discarded.
  if (screen === 'reportFailed') {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}
      >
        <Icon name="warning" size={64} color={colors.warning} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary, textAlign: 'center' }}>
          Your {asset} is on its way
        </Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          We could not reach our servers to confirm it. Your transaction reference is saved — retry
          now, or open the order and retry there.
        </Text>
        {error ? (
          <Text variant="caption" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>
            {error}
          </Text>
        ) : null}
        {broadcastHash ? (
          <Text variant="caption" numeric style={{ marginTop: spacing.lg, color: colors.textPrimary, textAlign: 'center' }}>
            {broadcastHash}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        {created && broadcastHash ? (
          <Button
            title="Retry"
            loading={reporting}
            onPress={() => reportHash(created.intentId, broadcastHash)}
          />
        ) : null}
        <View style={{ height: spacing.sm }} />
        {created ? (
          <Button
            title="Open this order"
            variant="secondary"
            onPress={() => router.replace(`/wallet/it/intent/${created.intentId}`)}
          />
        ) : null}
      </SafeAreaView>
    );
  }

  if (screen === 'error') {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.xl, alignItems: 'center', gap: spacing.sm }}>
          <Icon name="closeCircle" size={64} color={colors.danger} />
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
            Sell failed
          </Text>
          <Text variant="bodySm" style={{ color: colors.danger, textAlign: 'center' }}>
            {error}
          </Text>

          {/* We could not read a hash — which is NOT proof nothing was
              broadcast. Offer the recovery route rather than a dead end. */}
          {recoveryIntentId && created ? (
            <>
              <Text variant="bodySm" style={{ marginTop: spacing.md, color: colors.textSecondary, textAlign: 'center' }}>
                If your wallet shows the transfer went through, report it against this order rather
                than sending again.
              </Text>
              {reviewChainKey === 'polygon' ? (
                recoverySends === null ? (
                  <>
                    <Button
                      title={recoveryError ? 'Try again' : 'Find my transaction'}
                      variant="secondary"
                      loading={recoveryLoading}
                      onPress={findMyTransaction}
                    />
                    {/* A lookup that FAILED says so. "No transfer found" here
                        would tell someone whose funds did move that they did
                        not — the one sentence that produces a second send. */}
                    {recoveryError ? (
                      <Text variant="caption" style={{ color: colors.danger, textAlign: 'center' }}>
                        Couldn&apos;t check your history — this does not mean nothing was sent.
                      </Text>
                    ) : null}
                  </>
                ) : recoverySends.length === 0 ? (
                  <Text variant="caption" style={{ color: colors.textMuted, textAlign: 'center' }}>
                    No transfer to this order&apos;s deposit address in your recent Polygon history.
                    If your wallet shows one, paste its hash below.
                  </Text>
                ) : (
                  <View style={{ alignSelf: 'stretch', gap: spacing.xs }}>
                    {recoverySends.map((t) => (
                      <SelectRow
                        key={t.txHash}
                        label={`${t.amount} ${t.tokenSymbol}`}
                        value={shortHash(t.txHash)}
                        leftIcon="wallet"
                        disabled={reporting}
                        onPress={() => reportHash(recoveryIntentId, t.txHash)}
                      />
                    ))}
                  </View>
                )
              ) : null}

              {/* Every chain, not just Polygon. getTransactionHistory only
                  sees the DESTINATION chain, so a bridged send has no lookup
                  at all — and without this the instruction above ("report it
                  against this order") is one the app cannot honour. */}
              <View style={{ alignSelf: 'stretch', gap: spacing.xs }}>
                <Input
                  label="Already sent it? Paste your transaction hash"
                  value={manualHash}
                  onChangeText={(v) => {
                    setManualHash(v);
                    setManualHashError(null);
                  }}
                  placeholder="0x…"
                  autoCapitalize="none"
                  autoCorrect={false}
                  error={manualHashError ?? undefined}
                  helper={`From your wallet's history on ${getChain(reviewChainKey).name}.`}
                />
                <Button
                  title="Report this transaction"
                  variant="secondary"
                  loading={reporting}
                  disabled={!manualHash.trim()}
                  onPress={() => reportManualHash(recoveryIntentId)}
                />
              </View>

              <Button
                title="Open this order"
                variant="secondary"
                onPress={() => router.replace(`/wallet/it/intent/${recoveryIntentId}`)}
              />
            </>
          ) : null}

          <View style={{ height: spacing.lg }} />
          {/* No Back while a send may be in flight: it returns to the form
              with the intent still open, and a fresh quote + confirm from
              there mints a SECOND intent and a second send. "Open this order"
              above is the way out of this screen. */}
          {recoveryIntentId && created ? null : (
            <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
          )}
        </ScrollView>
      </SafeAreaView>
    );
  }

  if (screen === 'review') {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
            Review sell
          </Text>

          {/* First and prominent: without it the user is bounced back to a
              review screen showing the OLD figure with no explanation. */}
          {repricedTo !== null ? (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'flex-start',
                gap: spacing.sm,
                backgroundColor: colors.warningBg,
                borderRadius: radius.sm,
                padding: spacing.md,
              }}
            >
              <Icon name="warning" size={16} color={colors.warning} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                The rate moved. Your payout is now {formatKobo(repricedTo)} (was{' '}
                {formatKobo(repricedFrom)}). Confirm to accept.
              </Text>
            </View>
          ) : null}

          <Card>
            <SummaryCard>
              <SummaryRow label="You sell" value={`${reviewAmount} ${asset}`} />
              <SummaryRow label="From" value={reviewChainLabel} />
              {sendVsArrive ? <SummaryRow label="Bridge" value={sendVsArrive} /> : null}
              <Divider spacing="xs" />
              <SummaryRow label="Rate" value={`${formatKobo(reviewRateKobo)} / ${asset}`} />
              <SummaryRow label="Gross" value={formatKobo(reviewGrossKobo)} />
              <SummaryRow
                label="After spread"
                value={formatKobo(reviewAfterSpreadKobo)}
                hint={spreadKobo ? `Spread ${formatKobo(spreadKobo)}` : undefined}
              />
              <SummaryRow label="Fee" value={reviewFeeKobo ? `−${formatKobo(reviewFeeKobo)}` : '—'} />
              <Divider spacing="xs" />
              <SummaryRow
                label={reviewBridged ? 'You receive (estimate)' : 'You receive'}
                value={formatKobo(repricedTo ?? reviewNetKobo)}
                total
              />
            </SummaryCard>
          </Card>

          {/* On a bridged sell the ₦ figure is an ESTIMATE, not a fact: the
              payout is `received / quoted`, and LiFi's toAmount is itself an
              estimate — under-delivery scales the payout down. On Polygon there
              is no bridge, so the figure is firm and needs no qualifier. */}
          {reviewBridged ? (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'flex-start',
                gap: spacing.sm,
                backgroundColor: colors.warningBg,
                borderRadius: radius.sm,
                padding: spacing.md,
              }}
            >
              <Icon name="info" size={16} color={colors.warning} />
              <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                Bridging from {reviewChainLabel} to Polygon, so this payout is an estimate — you are
                paid on what actually arrives. {OVER_DELIVERY_NOTE}
              </Text>
            </View>
          ) : null}

          <Card>
            <SummaryCard>
              <SummaryRow label="Bank" value={reviewBankName || reviewBankCode} />
              <SummaryRow label="Account number" value={reviewAccountNumber} />
              <SummaryRow label="Account name" value={reviewAccountName} />
            </SummaryCard>
          </Card>

          {reviewExpiresAt ? (
            <Card>
              {/* Keyed on the expiry: CountdownBar fires onExpire once per
                  MOUNT, so a refreshed quote needs a fresh instance or a second
                  expiry would pass silently with the user still on a dead quote. */}
              <CountdownBar
                key={reviewExpiresAt}
                label="Quote valid for"
                expiresAt={reviewExpiresAt}
                onExpire={refreshExpiredQuote}
              />
            </Card>
          ) : null}

          {error ? (
            <Text variant="bodySm" style={{ color: colors.danger }}>
              {error}
            </Text>
          ) : null}

          {/* Create runs HERE, before the PIN: the reprice check has to happen
              while the user can still walk away from a moved figure. */}
          <Button
            title="Confirm sell"
            onPress={confirmSell}
            loading={creating}
            disabled={quoting || creating}
          />
          <Button
            title="Back"
            variant="ghost"
            disabled={creating}
            onPress={() => {
              setShowAuth(false);
              setError(null);
              setScreen('form');
            }}
          />
        </ScrollView>

        <AuthorizationModal
          visible={showAuth}
          title="Authorize sell"
          description={`Sell ${reviewAmount} ${asset} to ${reviewAccountName} (${reviewAccountNumber})`}
          onAuthorized={runSigning}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        keyboardShouldPersistTaps="handled"
      >
        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          Sell {asset} for Naira. Your {asset} goes straight from your wallet to our payout partner —
          we never hold it.
        </Text>

        <SummaryRow label="Asset" value={asset} />

        <SelectRow
          label="Send from"
          value={chainLabel}
          leftIcon="wallet"
          onPress={() => setShowChainPicker(true)}
        />

        <AmountInput
          label={`Amount of ${asset} to sell`}
          value={amount}
          onChangeText={(v) => {
            setAmount(v);
            setError(null);
          }}
          symbol={asset}
          symbolPosition="suffix"
          onMax={balance ? () => setAmount(truncateToStableDecimals(balance)) : undefined}
          helper={
            tokenLoading
              ? `Loading ${asset} on ${chainLabel}…`
              : balanceError
                ? balanceError
                : balance === null
                  ? `Balance on ${chainLabel} unavailable`
                  : // Truncated the same way Max is, so the figure shown and the
                    // figure Max fills in are the same number — toLocaleString
                    // would otherwise round the 7th digit up and disagree with it.
                    `Balance: ${Number(truncateToStableDecimals(balance)).toLocaleString(undefined, { maximumFractionDigits: STABLE_MAX_DECIMALS })} ${asset}`
          }
        />

        <BankDestination currency="NGN" value={dest} onChange={setDest} />

        {quoting ? <Spinner inline label="Pricing…" /> : null}

        {quoteError ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {quoteError}
          </Text>
        ) : null}

        {quote && arrival ? (
          <Card>
            <SummaryCard>
              {formSendVsArrive ? <SummaryRow label="Bridge" value={formSendVsArrive} /> : null}
              <SummaryRow
                label={bridged ? 'You receive (estimate)' : 'You receive'}
                value={formatKobo(quote.net)}
                total
              />
            </SummaryCard>
          </Card>
        ) : null}

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        <Button
          title="Review"
          onPress={goReview}
          disabled={!amount || !dest.ready || quoting || !quote}
        />

        <View
          style={{
            flexDirection: 'row',
            alignItems: 'flex-start',
            gap: spacing.sm,
            backgroundColor: colors.warningBg,
            borderRadius: radius.sm,
            padding: spacing.md,
          }}
        >
          <Icon name="warning" size={16} color={colors.warning} />
          <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
            You are paid on what actually arrives at our payout partner. Send from the chain you
            picked here — funds sent from anywhere else cannot be matched to this sell.
          </Text>
        </View>

        <Button title="Cancel" variant="ghost" onPress={() => router.back()} />
      </ScrollView>

      <PickerSheet
        visible={showChainPicker}
        onClose={() => setShowChainPicker(false)}
        title="Send from"
        items={chainItems}
        selectedId={chainKey}
        onSelect={(item) => {
          setShowChainPicker(false);
          setChainKey(item.id as ChainKey);
          setQuoteError(null);
          setError(null);
        }}
      />
    </SafeAreaView>
  );
}
