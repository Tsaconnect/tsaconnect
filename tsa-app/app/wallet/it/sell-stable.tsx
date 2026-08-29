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
// Flow: form → review → (Task 7) signing. This file owns `form` and `review`
// plus the review SNAPSHOT the signing step reads.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
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
import { parseTokenAmount } from '@/services/p2p';
import { getActiveWallet, readErc20BalanceOf } from '@/services/wallet';
import { getDiscoveredTokens } from '@/services/walletApi';
import {
  makeIdempotencyKey,
  quoteStableSell,
  type StableAsset,
  type StableQuote,
} from '@/services/itApi';
import {
  POLYGON_STABLE_DECIMALS,
  STABLE_MAX_DECIMALS,
  STABLE_MIN_SEND,
  belowMinimum,
  estimateArrival,
  exceedsStablePrecision,
  formatSendVsArrive,
} from '@/utils/stableSellQuote';

type Screen = 'form' | 'review' | 'signing' | 'error';

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

// The quote's money fields are minor-unit integer STRINGS and must stay that
// way — Number(kobo) on a large payout silently loses precision, and this is
// display-only formatting, so it is pure string math.
function formatKobo(kobo: string | null | undefined): string {
  if (!kobo) return '—';
  const negative = kobo.trim().startsWith('-');
  const digits = kobo.replace(/\D/g, '');
  if (!digits) return '—';
  const padded = digits.padStart(3, '0');
  const naira = padded.slice(0, -2).replace(/^0+(?=\d)/, '');
  const minor = padded.slice(-2);
  const grouped = naira.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return `${negative ? '-' : ''}₦${grouped}.${minor}`;
}

/**
 * Trim a major-unit asset amount to the precision the backend accepts.
 *
 * TRUNCATES, never rounds. `ethers.formatUnits` renders the balance at the
 * SOURCE token's decimals, and BSC's USDT/USDC are genuinely 18 — so the Max
 * button would otherwise fill in 18 fractional digits, `exceedsStablePrecision`
 * would reject the app's own value, and the one-tap path would fail on exactly
 * the chain this flow most has to get right. Rounding up instead of truncating
 * would push Max past the real balance and fail the send on-chain.
 *
 * String math, not `toFixed` — a float round-trip is what this guards against.
 */
function truncateToStableDecimals(major: string): string {
  const [whole, frac = ''] = major.split('.');
  const kept = frac.slice(0, STABLE_MAX_DECIMALS).replace(/0+$/, '');
  return kept ? `${whole}.${kept}` : whole;
}

/** gross − afterSpread, as a kobo string. Both are integers on the wire. */
function subtractKobo(a: string | null, b: string | null): string | null {
  if (!a || !b) return null;
  try {
    return (BigInt(a) - BigInt(b)).toString();
  } catch {
    return null;
  }
}

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
  // the "the rate moved" banner below. Task 7 Step 1 sets it.
  const [repricedTo, setRepricedTo] = useState<string | null>(null);

  // --- signing (filled in by Task 7) ---------------------------------------
  const [showAuth, setShowAuth] = useState(false);
  const [signingStatus, setSigningStatus] = useState<string | null>(null);
  // Set when a Polygon submit returned no hash: we cannot prove nothing was
  // broadcast, so the error screen offers a recovery route rather than a dead
  // end. Task 7 Step 4.
  const [recoveryHint, setRecoveryHint] = useState<string | null>(null);
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
    // become an unexplained locked form.
    if (balance !== null && Number(amount) > Number(balance)) {
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
  }, [asset, chainLabel, composeQuote, dest, enterReview, sourceToken]);

  // Filled in by Task 7 — the modal needs something to bind to. Failing loudly
  // beats a confirm button that silently does nothing.
  const runSigning = useCallback(async () => {
    setShowAuth(false);
    setSigningStatus(null);
    setRecoveryHint(null);
    setError('Signing is not available yet on this build.');
    setScreen('error');
  }, []);

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

  if (screen === 'error') {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}
      >
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>
          Sell failed
        </Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>
          {error}
        </Text>
        {recoveryHint ? (
          <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
            {recoveryHint}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
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
                The rate moved. Your payout is now {formatKobo(repricedTo)} (was {formatKobo(reviewNetKobo)}).
                Confirm to accept.
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

          <Button title="Confirm sell" onPress={() => setShowAuth(true)} disabled={quoting} />
          <Button
            title="Back"
            variant="ghost"
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
