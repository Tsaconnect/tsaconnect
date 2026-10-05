// app/p2p/create-ad.tsx
//
// Self-funding sell-ad publish flow ("hide the pool", spec 2026-06-05):
//
//   1. Resolve the seller wallet ONCE (with the verifyWalletKey pre-flight).
//   2. ensureEscrowFunding(...) — top up the seller's on-chain `available`
//      balance to cover THIS ad (approve + deposit + confirm as needed) on the
//      ad's selected chain. Skipped with no gas when `available` already covers.
//   3. POST /p2p/ads → returns { ad (pending), typedData }
//   4. signTypedData(typedData) using the same wallet — off-chain, no gas.
//   5. POST /p2p/ads/:id/sign with the signature → ad goes active.
//
// Funding runs FIRST so a published ad is always backed; if the deposit
// confirms but create/sign fails, the funds are safe in `available`. If the
// deposit broadcasts but its confirm POST fails, a recovery banner (keyed off
// the stashed RECORD) lets the seller retry the confirm without re-broadcasting.
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// The EIP-712 voucher signing flow, field validation, payment-method
// selection, and fixed/float rate logic are unchanged.

import { router } from 'expo-router';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  chainsApi,
  formatTokenAmount,
  paymentMethodsApi,
  poolApi,
  parseTokenAmount,
  p2pTokenDecimals,
  type P2PChainInfo,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { ensureEscrowFunding } from '../../services/escrowFunding';
import { fundingPreflight, publishActionForFunding } from '../../services/createAdFunding';
import { listPending, removePending, type PendingEscrowConfirm } from '../../services/p2pPending';
import { decodeRevertError } from '../../services/txErrors';
import { getActiveWallet, signTypedData, verifyWalletKey } from '../../services/wallet';
import { exchangeRateService } from '../../services/exchangeRateApi';
import { MAX_FLOAT_OFFSET_PCT, pctToBps } from '../../constants/p2p';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useIsPaymentMerchant } from '../../hooks/useIsPaymentMerchant';
import { PmPayoutFeeNotice } from '@/components/p2p/PmPayoutFeeNotice';
import { useCurrency } from '../../contexts/CurrencyContext';
import CurrencySelector from '../../components/currency/CurrencySelector';
import { getChainByChainId, getNetworkDisplayName } from '../../constants/chains';
import { getWalletBalances, normalizeWalletBalances } from '../../services/walletApi';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Chip,
  Divider,
  Icon,
  Input,
  PressableScale,
  SegmentedControl,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';

// Static fallback token list — used when the backend's /p2p/chains endpoint
// is unreachable so the screen never crashes with an empty token set. MCGP is
// deliberately absent: wallet MCGP may not be used in P2P (owner decision
// 2026-09-28), and the API refuses new MCGP ads.
const FALLBACK_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
];

type TokenEntry = { symbol: string; decimals: number };
type RateType = 'fixed' | 'float';

// Sonic chain IDs (mainnet + testnet). Used for the "Sonic-first" chain sort
// and the pool-balance caveat copy.
const SONIC_CHAIN_IDS = new Set([146, 14601]);

// P2P/PM platform fee charged to the merchant on a successful trade. Mirrors
// the on-chain FeatureP2PPlatform rate (1%) — kept in sync by review.
const P2P_PLATFORM_FEE_PCT = 0.01;

/** Return true if the given chainId is Sonic (either net). */
function isSonicChain(chainId: number): boolean {
  return SONIC_CHAIN_IDS.has(chainId);
}

/** Sort chains: Sonic first, then ascending chainId. Stable for equal positions. */
function sortChains(chains: P2PChainInfo[]): P2PChainInfo[] {
  return [...chains].sort((a, b) => {
    const aS = isSonicChain(a.chainId) ? 0 : 1;
    const bS = isSonicChain(b.chainId) ? 0 : 1;
    if (aS !== bS) return aS - bS;
    return a.chainId - b.chainId;
  });
}

const CreateAdScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const kbHeight = useKeyboardHeight();

  // Currency for THIS ad is a local form field. We seed it from the P2P market
  // currency (do not mutate the hook's setCurrency here), then resolve the full
  // config off supportedCurrencies so we have the right symbol for display.
  const { currency: marketCurrency } = useP2pCurrency();
  const { supportedCurrencies } = useCurrency();
  // Active PMs settle via their Nomba VA (buyers pay the VA, auto-settled), so
  // they don't attach a manual payment method — the selector is replaced by an
  // info card and the publish requirement is waived for them.
  const { isPm, payoutFees } = useIsPaymentMerchant();
  const [adCurrencyCode, setAdCurrencyCode] = useState<string | null>(null);
  const currencyCode = (adCurrencyCode ?? marketCurrency.code).toUpperCase();
  const currencyConfig =
    supportedCurrencies.find((c) => c.code === currencyCode) ?? marketCurrency;
  const symbol = currencyConfig.symbol;

  // ── Chain / token data from the backend ─────────────────────────────────
  // enabledChains: the P2P-enabled chains returned by /p2p/chains. Empty
  // until the fetch completes; falls back to FALLBACK_TOKENS on failure.
  const [enabledChains, setEnabledChains] = useState<P2PChainInfo[]>([]);
  const [chainsFetched, setChainsFetched] = useState(false);

  // Derive the selectable token list from the union of assets across all
  // enabled chains, preserving decimal metadata from FALLBACK_TOKENS.
  const availableTokens: TokenEntry[] = useMemo(() => {
    if (!chainsFetched || enabledChains.length === 0) return FALLBACK_TOKENS;
    const symbols = new Set(enabledChains.flatMap((c) => c.assets));
    return Array.from(symbols).map((sym) => {
      const fallback = FALLBACK_TOKENS.find((t) => t.symbol === sym);
      return fallback ?? { symbol: sym, decimals: 18 };
    });
  }, [enabledChains, chainsFetched]);

  const [token, setToken] = useState<TokenEntry>(FALLBACK_TOKENS[0]);
  const [amountInput, setAmountInput] = useState(''); // human decimal
  const [rateType, setRateType] = useState<RateType>('fixed');
  const [rateInput, setRateInput] = useState('');
  // Signed decimal percent, e.g. "0.5" = +0.5%, "-1.25" = -1.25%. Converted
  // to basis points on submit. Only consulted when rateType==='float'.
  const [floatOffsetPctInput, setFloatOffsetPctInput] = useState('');
  const [minLimitInput, setMinLimitInput] = useState('');
  const [maxLimitInput, setMaxLimitInput] = useState('');
  // Keep the full fetched list; the visible list is derived by currency below.
  const [allPaymentMethods, setAllPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmIds, setSelectedPmIds] = useState<string[]>([]);
  const [loadingPms, setLoadingPms] = useState(true);
  const [suggestedRate, setSuggestedRate] = useState<number | null>(null);
  const [rateStale, setRateStale] = useState(false);
  // Whether the selected currency has a Bybit P2P anchor — float ads require one.
  const [floatSupported, setFloatSupported] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState<string>('');
  // On-chain `available` balance for the selected token+chain. Seeds the
  // funding-summary math (shortfall = adAmount − available) so the seller sees
  // exactly what will happen before they publish. `null` = not yet loaded;
  // `'-'` = load failed (we don't block submit on a transient read failure —
  // ensureEscrowFunding re-reads the authoritative balance at publish time).
  const [poolAvailable, setPoolAvailable] = useState<string | null>(null);

  // Recovery: any deposit that broadcast on-chain but whose server-side confirm
  // failed is stashed (services/p2pPending.ts) and surfaced as a banner. Keyed
  // off the RECORD's own {token, chainId, amount, txHash}, NOT the live form —
  // a stranded deposit must be visible even if the seller re-selects a
  // different token/chain. There is no server-side deposit reconciler.
  const [pendingDeposits, setPendingDeposits] = useState<PendingEscrowConfirm[]>([]);
  const [retryingTxHash, setRetryingTxHash] = useState<string | null>(null);

  // Monotonic id for in-flight pool-balance reads — a chain/token switch
  // mid-fetch must not let a slower wrong-chain response clobber the latest
  // one and seed the shortfall math with the wrong chain's balance.
  const balReqRef = useRef(0);

  // ── Chain selector state ──────────────────────────────────────────────
  // chainsForToken: sorted subset of enabledChains that offer the current token.
  // selectedChainId: null when the chooser is hidden (single-chain token); the
  // first chain in chainsForToken otherwise. undefined = not yet resolved.
  const chainsForToken: P2PChainInfo[] = useMemo(() => {
    if (enabledChains.length === 0) return [];
    return sortChains(enabledChains.filter((c) => c.assets.includes(token.symbol)));
  }, [enabledChains, token.symbol]);

  const [selectedChainId, setSelectedChainId] = useState<number | undefined>(undefined);

  // When the chainsForToken list changes (token switch or after fetch), reset
  // the selected chain to the first in the sorted list.
  useEffect(() => {
    if (chainsForToken.length > 0) {
      setSelectedChainId(chainsForToken[0].chainId);
    } else {
      // No chain data yet (before fetch or on fallback path) — leave undefined
      // so we omit chainId from the create call and let the backend default.
      setSelectedChainId(undefined);
    }
  }, [chainsForToken]);

  // The chain config for display (human name, etc.). May be undefined for
  // unknown chainIds not in CHAINS (e.g. a new chain the app doesn't yet know).
  // Memoized: getChainByChainId returns a fresh object every call, so without
  // this the reference changes each render and any effect depending on it (the
  // funding-summary effect) re-runs in a loop, flickering the summary text.
  const selectedChainConfig = useMemo(
    () => (selectedChainId != null ? getChainByChainId(selectedChainId) : undefined),
    [selectedChainId],
  );

  // Show the chain chooser only when:
  //  (a) we have a valid backend response, AND
  //  (b) more than one chain offers the current token.
  const showChainChooser = chainsFetched && chainsForToken.length > 1;

  // Load all payment methods once on mount; the visible subset is filtered by
  // the selected currency below. Always clears the loading flag — a network
  // reject must not leave the PM picker spinning forever.
  useEffect(() => {
    (async () => {
      try {
        const res = await paymentMethodsApi.list();
        if (res.success && res.data) {
          setAllPaymentMethods(res.data);
        }
      } catch (err) {
        console.warn('[create-ad] paymentMethodsApi.list failed', err);
      } finally {
        setLoadingPms(false);
      }
    })();
  }, []);

  // Fetch the P2P-enabled chains + their assets on mount. On failure we
  // degrade gracefully to the hardcoded FALLBACK_TOKENS — the screen stays
  // functional, just without a chain chooser.
  useEffect(() => {
    (async () => {
      try {
        const res = await chainsApi.list();
        if (res.success && res.data && res.data.length > 0) {
          setEnabledChains(res.data);
        }
        // On failure or empty list, leave enabledChains empty — the useMemo
        // above falls back to FALLBACK_TOKENS automatically.
      } catch (err) {
        console.warn('[create-ad] chainsApi.list failed, using fallback tokens', err);
      } finally {
        setChainsFetched(true);
      }
    })();
  }, []);

  // Suggested Bybit rate + float eligibility for the SELECTED currency.
  // Re-runs whenever the ad currency changes.
  useEffect(() => {
    (async () => {
      try {
        const res = await exchangeRateService.getRates();
        const row = res.rates?.[currencyCode];
        setSuggestedRate(row?.binanceSell ?? null); // seller rate (sells token → buyer pays fiat)
        setRateStale(!!row?.stale);
        setFloatSupported(!!row?.floatAnchor);
      } catch (err) {
        // Float-mode users will see "Live rate unavailable" — that's fine
        // for a feed outage; just log so post-mortem has a breadcrumb.
        console.warn('[create-ad] getRates failed', err);
        setSuggestedRate(null);
        setFloatSupported(false);
      }
    })();
  }, [currencyCode]);

  // A payment method for one currency must not stay selected when the ad
  // currency changes (e.g. an NGN method on a CAD ad).
  useEffect(() => {
    setSelectedPmIds([]);
  }, [currencyCode]);

  // If float becomes unsupported for the chosen currency, force fixed so we
  // never leave the form in an unpublishable float state.
  useEffect(() => {
    if (!floatSupported && rateType === 'float') setRateType('fixed');
  }, [floatSupported, rateType]);

  // Refresh the on-chain `available` balance whenever the token or chain
  // changes. This seeds the funding-summary shortfall math, so it MUST be
  // pinned to the chain it was read on: a chain switch mid-fetch fires a fresh
  // read while a prior one is still in flight, and without the monotonic
  // balReqRef guard the slower (wrong-chain) response could land last and seed
  // the shortfall against the wrong chain's balance. Only the latest request
  // may touch state. Doesn't block submit on failure — ensureEscrowFunding
  // re-reads the authoritative balance at publish time.
  useEffect(() => {
    const reqId = ++balReqRef.current;
    setPoolAvailable(null);
    (async () => {
      const walletAddr = await getActiveWallet();
      if (balReqRef.current !== reqId) return;
      if (!walletAddr) {
        setPoolAvailable('-');
        return;
      }
      const res = await poolApi.balance(token.symbol, walletAddr, selectedChainId ?? undefined);
      if (balReqRef.current !== reqId) return;
      if (res.success && res.data) {
        setPoolAvailable(res.data.available);
      } else {
        setPoolAvailable('-');
      }
    })();
  }, [token.symbol, selectedChainId]);

  // Spendable WALLET balance of the selected token on the selected chain, for
  // the ACTIVE wallet — the exact balance the deposit's transferFrom pulls from.
  // Surfaced so the seller can confirm they actually hold the token (and on the
  // right chain/wallet) before publishing. Read via the same RPC-backed
  // /wallet/balances the wallet screen uses; best-effort + stale-guarded; never
  // blocks publish. `walletBalanceHuman` is a human-decimal string ('-' = read
  // failed, null = loading / no chain yet).
  const [walletBalanceHuman, setWalletBalanceHuman] = useState<string | null>(null);
  const [activeWalletAddr, setActiveWalletAddr] = useState<string>('');
  const walletBalReqRef = useRef(0);
  useEffect(() => {
    const reqId = ++walletBalReqRef.current;
    setWalletBalanceHuman(null);
    (async () => {
      const addr = await getActiveWallet();
      if (walletBalReqRef.current !== reqId) return;
      setActiveWalletAddr(addr ?? '');
      if (!addr || selectedChainId == null) {
        // No wallet, or no resolved chain yet — leave loading; the chain
        // selector / wallet setup is the next thing the user sees.
        setWalletBalanceHuman(addr ? null : '-');
        return;
      }
      const chainKey = getChainByChainId(selectedChainId)?.key;
      try {
        const res = await getWalletBalances(selectedChainId, addr);
        if (walletBalReqRef.current !== reqId) return;
        if (res.success && res.data) {
          const entry = normalizeWalletBalances(res.data, chainKey).find(
            (e) => e.symbol.toUpperCase() === token.symbol.toUpperCase(),
          );
          // entry present → real balance (incl. a genuine "0"); entry absent →
          // token isn't registered for balance reads on this chain, which is
          // "unknown", not "0" — don't imply an empty wallet.
          setWalletBalanceHuman(entry ? entry.balance : '-');
        } else {
          setWalletBalanceHuman('-');
        }
      } catch {
        if (walletBalReqRef.current === reqId) setWalletBalanceHuman('-');
      }
    })();
  }, [token.symbol, selectedChainId]);

  // Surface any stranded deposit-confirm for the active wallet on mount (and
  // after a pending-confirm result). Reads by RECORD, not by the live form, so
  // a deposit broadcast on a chain/token the seller has since navigated away
  // from is still visible and retryable.
  const refreshPendingDeposits = React.useCallback(async () => {
    const walletAddr = await getActiveWallet();
    if (!walletAddr) {
      setPendingDeposits([]);
      return;
    }
    setPendingDeposits(await listPending('deposit', walletAddr));
  }, []);

  useEffect(() => {
    refreshPendingDeposits();
  }, [refreshPendingDeposits]);

  // Payment methods visible for the selected currency.
  const paymentMethods = useMemo(
    () => allPaymentMethods.filter((m) => m.currency?.toUpperCase() === currencyCode),
    [allPaymentMethods, currencyCode],
  );

  const togglePm = (id: string) => {
    setSelectedPmIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]
    );
  };

  // Effective rate previewed to the seller. Fixed mode: the literal fiat
  // rate the seller typed. Float mode: anchor × (1 + offset%/100), null if the feed is
  // missing or stale. Floats here are display-only; the server recomputes
  // at trade open with big.Rat precision.
  const effectiveRate = useMemo(() => {
    if (rateType === 'fixed') {
      const r = parseFloat(rateInput || '0');
      return isFinite(r) && r > 0 ? r : null;
    }
    if (suggestedRate == null || rateStale) return null;
    const pct = parseFloat(floatOffsetPctInput || '0');
    if (!isFinite(pct)) return null;
    return suggestedRate * (1 + pct / 100);
  }, [rateType, rateInput, suggestedRate, rateStale, floatOffsetPctInput]);

  const previewFiat = useMemo(() => {
    const amt = parseFloat(amountInput || '0');
    if (!isFinite(amt) || amt <= 0 || effectiveRate == null) return 0;
    return amt * effectiveRate;
  }, [amountInput, effectiveRate]);

  // Float-mode validation: offset must parse, be within ±cap, and the
  // reference feed must be fresh — we can't publish a float ad without a
  // live anchor.
  const floatOffsetPctValid = useMemo(() => {
    if (rateType !== 'float') return true;
    const pct = parseFloat(floatOffsetPctInput);
    if (!isFinite(pct)) return false;
    return Math.abs(pct) <= MAX_FLOAT_OFFSET_PCT;
  }, [rateType, floatOffsetPctInput]);

  // Per-chain decimals for the selected token+chain. Used for ALL funding math
  // (shortfall parse, deposit amount, confirm amount, and display) — NEVER
  // token.decimals, which is the chain-blind hardcoded-6 FALLBACK value and
  // would under-fund an 18-dec BSC deposit by 1e12 (spec "Review fixes" §1).
  const adDecimals = p2pTokenDecimals(selectedChainId, token.symbol);

  // The ad amount in base units, or null when the human input isn't a clean
  // positive decimal. Drives both the funding summary and the deposit amount.
  const adAmountBaseUnits = useMemo(() => {
    const trimmed = amountInput.trim();
    if (!/^\d+(\.\d+)?$/.test(trimmed)) return null;
    try {
      const base = parseTokenAmount(trimmed, adDecimals);
      return base === '0' ? null : base;
    } catch {
      return null;
    }
  }, [amountInput, adDecimals]);

  // Allowance-aware funding summary computed before submit (spec "Review
  // fixes" §6/§7): shortfall ≤ 0 → already covered (no deposit); else we'll
  // deposit the shortfall, and we only promise an approval when the current
  // allowance doesn't already cover it. `null` until the inputs are valid.
  const [fundingSummary, setFundingSummary] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setFundingSummary(null);
    if (adAmountBaseUnits == null) return;
    if (poolAvailable == null) return; // balance still loading
    (async () => {
      const sym = token.symbol;
      const need = BigInt(adAmountBaseUnits);
      const have = poolAvailable && poolAvailable !== '-' ? BigInt(poolAvailable) : 0n;
      const shortfall = need > have ? need - have : 0n;
      const lockedHuman = formatTokenAmount(adAmountBaseUnits, adDecimals, 4);
      const chainName = selectedChainConfig?.shortName ?? 'the selected network';

      if (shortfall <= 0n) {
        const haveHuman = formatTokenAmount(have.toString(), adDecimals, 4);
        if (!cancelled) {
          setFundingSummary(
            `This ad locks ${lockedHuman} ${sym} in escrow, already covered by your ` +
              `${haveHuman} available — no deposit needed.`,
          );
        }
        return;
      }

      const depositHuman = formatTokenAmount(shortfall.toString(), adDecimals, 4);

      // Only promise an approval when the live allowance doesn't already cover
      // the shortfall. A read failure / non-numeric allowance falls back to
      // the conservative "one approval + one deposit" wording.
      let needsApprove = true;
      if (selectedChainId != null) {
        try {
          const aRes = await poolApi.allowance(sym, await getActiveWallet() ?? '', selectedChainId);
          if (aRes.success && aRes.data) {
            needsApprove = BigInt(aRes.data.allowance) < shortfall;
          }
        } catch {
          // keep the conservative default
        }
      }
      if (cancelled) return;
      const steps = needsApprove
        ? `one approval + one deposit on ${chainName}`
        : `one deposit on ${chainName}`;
      setFundingSummary(
        `This ad locks ${lockedHuman} ${sym} in escrow. You'll deposit ${depositHuman} ${sym} ` +
          `now (${steps}).`,
      );
    })();
    return () => {
      cancelled = true;
    };
  }, [adAmountBaseUnits, poolAvailable, adDecimals, token.symbol, selectedChainId, selectedChainConfig]);

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    (isPm || selectedPmIds.length > 0) &&
    (rateType === 'fixed'
      ? parseFloat(rateInput || '0') > 0
      : floatOffsetPctValid && floatSupported && suggestedRate != null && !rateStale);

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
    // Tracks whether a deposit actually broadcast+confirmed this attempt, so the
    // catch only tells the seller "your deposit is safe" when one really happened
    // (an insufficient-balance / gas-estimate revert moves no funds at all).
    let depositedThisAttempt = false;
    try {
      // 1. Resolve seller wallet address.
      setSubmitStage('Loading wallet…');
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      // Pre-flight integrity check: the stored private key must actually
      // derive to sellerAddress. Catches SecureStore corruption (legacy
      // migration crossed-wires, re-import under wrong slot) *before* the
      // backend creates a pending ad row — without this guard the same
      // mismatch surfaces later as a cryptic "signature recovered to X,
      // expected Y" 400 from SignAd, and we'd leave an orphan pending ad
      // in the DB on every retry.
      const integrity = await verifyWalletKey(sellerAddress);
      if (!integrity.ok) {
        if (integrity.reason === 'missing') {
          throw new Error(
            `No private key stored for wallet ${sellerAddress}. ` +
              'Remove this wallet and re-import it from its seed phrase.'
          );
        }
        throw new Error(
          `Wallet ${sellerAddress} is corrupt — its stored key actually ` +
            `derives to ${integrity.derived}. Remove this wallet from the ` +
            'wallet list and re-import it from its seed phrase, then retry.'
        );
      }

      // 2. Convert human inputs to base units / canonical strings. Decimals
      // MUST come from p2pTokenDecimals(selectedChainId, …) — NOT token.decimals
      // (the chain-blind hardcoded-6 FALLBACK value), or an 18-dec BSC deposit
      // would be under-funded by 1e12 (spec "Review fixes" §1).
      const tokenAmount = parseTokenAmount(amountInput, adDecimals);
      const minLimit = minLimitInput.trim() ? minLimitInput.trim() : undefined;
      const maxLimit = maxLimitInput.trim() ? maxLimitInput.trim() : undefined;

      // 3. Fund the seller's on-chain `available` to cover THIS ad before we
      // create+sign+activate, so a published ad is always backed. ensureFunding
      // is a real on-chain broadcast (approve+deposit as needed), so it must
      // never run on an unresolved/defaulted chain: a deposit on the wrong
      // chain would desync the voucher domain. Require a concrete chainId
      // (spec "Review fixes" §2) — selectedChainId is undefined for a
      // single-chain token or before /p2p/chains lands; in either case we'd be
      // about to deposit something, so block until a chain is resolved.
      const preflight = fundingPreflight({
        amountBaseUnits: tokenAmount,
        available: poolAvailable,
        selectedChainId,
        chainsFetched,
      });
      if (preflight.kind === 'block') {
        throw new Error(preflight.message);
      }
      if (preflight.kind === 'fund') {
        const fundResult = await ensureEscrowFunding({
          token: token.symbol,
          chainId: preflight.chainId,
          amountBaseUnits: tokenAmount,
          walletAddress: sellerAddress,
          onStage: setSubmitStage,
          onGasAlert: (m) => Alert.alert('Need gas', m),
        });
        const action = publishActionForFunding(fundResult);
        if (action.kind === 'abort') {
          // Nothing signed, nothing dangling (e.g. no native gas) — stop cleanly.
          return;
        }
        if (action.kind === 'recover') {
          // The deposit is on-chain & safe in `available`, but the confirm POST
          // failed. Do NOT publish an unbacked ad — surface the recovery banner
          // and let the seller retry the confirm, then re-publish.
          await refreshPendingDeposits();
          Alert.alert(
            'Deposit sent — confirm pending',
            'Your deposit broadcast on-chain and your funds are safe in escrow, but ' +
              'the server could not record it yet. Use “Retry confirm” at the top of ' +
              'the screen, then publish again.',
          );
          return;
        }
        // action.kind === 'proceed' → fall through to create+sign+activate.
        // Record whether a real deposit landed this attempt (vs already-funded),
        // so a later create/sign failure can correctly say funds are safe.
        if (fundResult.status === 'funded') depositedThisAttempt = true;
      }

      // 4. Create ad (prepare phase). Funding (step 3) is already done, so the
      // ad's `available` balance is guaranteed to back it. Ask the backend to
      // create the ad in `pending` state; the server stamps the voucher fields
      // and returns an EIP-712 typed-data payload for the seller to sign.
      // Fixed mode sends the literal rate; float mode sends the offset bps.
      setSubmitStage('Building voucher…');
      const basePayload = {
        token: token.symbol,
        tokenAmount,
        fiatCurrency: currencyCode,
        minLimit,
        maxLimit,
        paymentMethodIds: selectedPmIds,
        sellerAddress,
        // selectedChainId is undefined when the fetch hasn't landed or when
        // only one chain exists (auto-selected). Omitting it lets the backend
        // default to Sonic, which is the correct single-chain behaviour.
        ...(selectedChainId != null ? { chainId: selectedChainId } : {}),
      };
      const createRes = await adsApi.create(
        rateType === 'fixed'
          ? { ...basePayload, rate: rateInput.trim() }
          : {
              ...basePayload,
              rateType: 'float',
              floatOffsetBps: pctToBps(parseFloat(floatOffsetPctInput)),
            }
      );
      if (!createRes.success || !createRes.data) {
        throw new Error(createAdStatusError(createRes.status, createRes.message));
      }
      const { ad, typedData } = createRes.data;

      // 5. Sign the voucher off-chain via EIP-712. No gas, no broadcast.
      // Pin signing to sellerAddress so the signature is guaranteed to
      // recover to the wallet the ad declares — otherwise a stale active
      // wallet (or a switch between create + sign) makes the backend reject
      // the signature with the cryptic "recovered != expected" error.
      setSubmitStage('Signing voucher…');
      const signature = await signTypedData(
        typedData.domain,
        typedData.types,
        typedData.message,
        sellerAddress,
      );

      // 6. Submit the signature back. Server verifies the recovered signer
      // matches `sellerAddress` and flips the ad to `active`.
      setSubmitStage('Activating ad…');
      const signRes = await adsApi.sign(ad.id, signature, sellerAddress);
      if (!signRes.success || !signRes.data) {
        throw new Error(createAdStatusError(signRes.status, signRes.message));
      }
      // The success alert was firing on any 2xx — but a server-side regression
      // could return {success:true, data:{...status: 'pending'}} if signature
      // verification silently no-op'd. Confirm the ad actually went live before
      // telling the user it did.
      if (signRes.data.status !== 'active') {
        throw new Error(
          `Server returned ad status "${signRes.data.status}" — refresh My Ads and contact support if it stays this way.`
        );
      }

      Alert.alert('Ad published', `Your ad is now live in the marketplace.`, [
        { text: 'OK', onPress: () => router.back() },
      ]);
    } catch (err: any) {
      const raw = err?.message || String(err);
      // Turn an opaque on-chain revert (e.g. the ERC20InsufficientBalance hex the
      // backend surfaces when the wallet can't cover the deposit) into a clear,
      // actionable message instead of raw ABI bytes.
      const revert = decodeRevertError(raw);
      let body: string;
      if (revert?.kind === 'insufficient-balance') {
        body =
          `Your wallet doesn't have enough ${token.symbol} to back this ad. It needs ` +
          `${formatTokenAmount(revert.needed, adDecimals, 4)} ${token.symbol}, but your wallet holds ` +
          `${formatTokenAmount(revert.balance, adDecimals, 4)} ${token.symbol} on this network. ` +
          `Top up your ${token.symbol}, then publish.`;
      } else if (revert?.kind === 'insufficient-allowance') {
        body =
          `The escrow isn't approved to move your ${token.symbol} yet. Please try publishing ` +
          `again — the approval is requested first.`;
      } else if (revert?.kind === 'unknown-revert') {
        body =
          `The deposit can't go through right now — your wallet may be missing the ${token.symbol} ` +
          `or the network gas needed. Check your balances and try again.`;
      } else {
        body = raw;
      }
      // Only mention a safe deposit when one actually broadcast+confirmed this
      // attempt; an insufficient-balance / gas-estimate revert moves no funds.
      if (depositedThisAttempt) {
        body +=
          '\n\nThe deposit you just made is safe in your escrow balance — retry to publish, ' +
          'or reclaim it from My Ads.';
      }
      Alert.alert('Could not publish ad', body);
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  // Retry a stranded deposit-confirm. Confirms against the RECORD's own fields
  // (token/amount/txHash/wallet/chain) — never the live form — so a deposit
  // broadcast on a chain/token the seller has since left is still recoverable.
  // No re-broadcast: the on-chain deposit already happened; this only records
  // it server-side (confirmDeposit is idempotent).
  const handleRetryConfirm = async (rec: PendingEscrowConfirm) => {
    if (retryingTxHash) return;
    setRetryingTxHash(rec.txHash);
    try {
      const conf = await poolApi.confirmDeposit(
        rec.token,
        rec.amount,
        rec.txHash,
        rec.walletAddress,
        rec.chainId,
      );
      if (!conf.success) {
        throw new Error(conf.message || `Confirm returned ${conf.status ?? 'no status'}`);
      }
      await removePending(rec.txHash);
      await refreshPendingDeposits();
      Alert.alert('Confirmed', 'Your deposit has been recorded. You can publish now.');
    } catch (err: any) {
      Alert.alert('Retry failed', err?.message || 'Server still cannot record this deposit.');
    } finally {
      setRetryingTxHash(null);
    }
  };

  // createAdStatusError turns the backend's HTTP status into actionable user
  // guidance. 410 (Gone) is the Phase 2B migration sentinel for stale clients
  // calling removed legacy endpoints; 503 fires when the rate feed is stale
  // mid-publish (float ads need a fresh feed). Anything else passes through.
  function createAdStatusError(status: number | undefined, message: string | undefined): string {
    if (status === 410) {
      return 'Your app version is out of date — please update and retry.';
    }
    if (status === 503) {
      return 'Live rate just expired. Refresh the screen and try again.';
    }
    // Server-side EIP-712 recovery mismatch — the wallet that produced the
    // signature derives to a different address than the one we declared as
    // sellerAddress. The pre-flight in handleSubmit catches the common
    // SecureStore-corruption case, but if a stale build slips through (or
    // the active wallet flips mid-flow) we still want a useful message
    // instead of the raw "signature recovered to 0x… expected 0x…" alert.
    if (message && /signature recovered to /i.test(message)) {
      return (
        'Your wallet did not sign the ad correctly — its key does not match ' +
        'the address on the ad. Remove this wallet and re-import it from its ' +
        'seed phrase, then retry. (Server message: ' + message + ')'
      );
    }
    return message || 'Server rejected the request';
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Create sell ad
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            You&apos;re offering {token.symbol} for {currencyCode}. We&apos;ll lock the amount in
            escrow as part of publishing — depositing only what isn&apos;t already covered — then
            you sign a voucher to authorise trades.
          </Text>

          {/* ── Recovery banner ──────────────────────────────────────────
              A deposit that broadcast on-chain but whose server confirm
              failed. Keyed off the stashed RECORD's own fields (token / chain
              / amount / txHash), never the live form, so a stranded deposit is
              always visible. "Retry confirm" records it server-side without
              re-broadcasting (the on-chain deposit already happened). */}
          {pendingDeposits.map((rec) => {
            const recChain = getChainByChainId(rec.chainId);
            const recDecimals = p2pTokenDecimals(rec.chainId, rec.token);
            const amtHuman = formatTokenAmount(rec.amount, recDecimals, 4);
            const isRetrying = retryingTxHash === rec.txHash;
            return (
              <AccentCard
                key={rec.txHash}
                railColor={colors.danger}
                padding="md"
                style={{ marginTop: spacing.md }}
              >
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Icon name="warning" size={18} color={colors.danger} />
                  <Text variant="bodyMed" weight="700" color="danger">
                    Deposit confirm pending
                  </Text>
                </View>
                <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                  A {amtHuman} {rec.token} deposit on {recChain?.shortName ?? `chain ${rec.chainId}`}{' '}
                  broadcast on-chain but the server couldn&apos;t record it. Your funds are safe in
                  escrow — retry to record it, then publish.
                </Text>
                <Text variant="caption" color="textMuted" numeric numberOfLines={1} style={{ marginTop: spacing.sm }}>
                  tx {rec.txHash}
                </Text>
                <View style={{ marginTop: spacing.md }}>
                  <Button
                    title={isRetrying ? 'Retrying…' : 'Retry confirm'}
                    size="sm"
                    fullWidth={false}
                    loading={isRetrying}
                    disabled={!!retryingTxHash}
                    onPress={() => handleRetryConfirm(rec)}
                  />
                </View>
              </AccentCard>
            );
          })}

          {/* ── Currency ─────────────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Currency
          </Text>
          <CurrencySelector
            variant="card"
            selectedCode={currencyCode}
            onSelectCode={(code) => setAdCurrencyCode(code)}
          />

          {/* ── Token ────────────────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Token
          </Text>
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
            {availableTokens.map((t) => (
              <Chip
                key={t.symbol}
                label={t.symbol}
                selected={token.symbol === t.symbol}
                onPress={() => setToken(t)}
              />
            ))}
          </View>

          {/* ── Chain ────────────────────────────────────────────────── */}
          {showChainChooser && (
            <>
              <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
                Network
              </Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                {chainsForToken.map((c) => (
                  <Chip
                    key={c.chainId}
                    label={getNetworkDisplayName(c.chainId)}
                    selected={selectedChainId === c.chainId}
                    onPress={() => setSelectedChainId(c.chainId)}
                  />
                ))}
              </View>
            </>
          )}

          {/* ── Amount ───────────────────────────────────────────────── */}
          <View style={{ marginTop: spacing.lg }}>
            <AmountInput
              label={`Amount (${token.symbol})`}
              placeholder="0.00"
              symbol={token.symbol}
              symbolPosition="suffix"
              value={amountInput}
              onChangeText={setAmountInput}
            />
            {/* Spendable wallet balance for the ACTIVE wallet on the selected
                chain — the exact balance the deposit pulls from. Lets the seller
                confirm they hold the token (right chain + right wallet) and warns
                when it can't cover the ad, instead of a post-publish revert. */}
            {(() => {
              if (walletBalanceHuman == null) {
                return (
                  <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.sm }}>
                    Checking wallet balance…
                  </Text>
                );
              }
              if (walletBalanceHuman === '-') {
                return (
                  <Text variant="caption" color="textMuted" style={{ marginTop: spacing.sm }}>
                    Wallet balance unavailable
                  </Text>
                );
              }
              const walNum = parseFloat(walletBalanceHuman) || 0;
              const amtNum = parseFloat(amountInput || '0') || 0;
              const low = amtNum > 0 && walNum < amtNum;
              const short = activeWalletAddr
                ? ` · ${activeWalletAddr.slice(0, 6)}…${activeWalletAddr.slice(-4)}`
                : '';
              return (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.sm }}>
                  <Icon name="wallet" size={14} color={low ? colors.danger : colors.textMuted} />
                  <Text variant="caption" color={low ? 'danger' : 'textSecondary'} style={{ flex: 1 }}>
                    Wallet: {walNum.toLocaleString(undefined, { maximumFractionDigits: 4 })} {token.symbol}
                    {short}
                    {low ? ` — not enough to back ${amountInput} ${token.symbol}` : ''}
                  </Text>
                </View>
              );
            })()}
            {/* Allowance-aware funding summary: tells the seller exactly what
                publishing will do (no deposit when `available` already covers
                the ad; otherwise the shortfall deposit + whether an approval is
                needed). Computed async from the on-chain balance + allowance. */}
            {fundingSummary && (
              <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.xs, marginTop: spacing.sm }}>
                <Icon name="info" size={14} color={colors.accent} />
                <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                  {fundingSummary}
                </Text>
              </View>
            )}
          </View>

          {/* ── Rate type ────────────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Rate type
          </Text>
          <SegmentedControl<RateType>
            segments={[
              { key: 'fixed', label: 'Fixed' },
              { key: 'float', label: 'Float (auto-update)' },
            ]}
            value={rateType}
            onChange={(k) => {
              // Float requires a live Bybit anchor; ignore the tap if the
              // selected currency has none (mirrors the old disabled segment).
              if (k === 'float' && !floatSupported) return;
              setRateType(k);
            }}
            style={!floatSupported ? { opacity: 0.85 } : undefined}
          />
          {!floatSupported && (
            <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
              Float rate isn&apos;t available for {currencyCode} — use a fixed rate.
            </Text>
          )}

          {/* ── Rate input (fixed | float) ───────────────────────────── */}
          {rateType === 'fixed' ? (
            <View style={{ marginTop: spacing.lg }}>
              <Input
                label={`Rate (${currencyCode} per 1 ${token.symbol})`}
                placeholder={suggestedRate ? String(suggestedRate) : '0.00'}
                keyboardType="decimal-pad"
                value={rateInput}
                onChangeText={setRateInput}
                leftAdornment={
                  <Text variant="bodyMed" weight="700" color="textSecondary">
                    {symbol}
                  </Text>
                }
              />
              {suggestedRate !== null && (
                <View
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.xs,
                    marginTop: spacing.sm,
                  }}
                >
                  <Icon name="info" size={14} color={rateStale ? colors.danger : colors.accent} />
                  <Text variant="caption" color={rateStale ? 'danger' : 'textSecondary'} style={{ flex: 1 }}>
                    {rateStale
                      ? 'Bybit rate stale — verify before publishing'
                      : `Current Bybit P2P sell rate: ${symbol}${suggestedRate.toFixed(2)}`}
                  </Text>
                  <PressableScale onPress={() => setRateInput(String(suggestedRate))}>
                    <Text variant="caption" weight="700" color="accent">
                      Use
                    </Text>
                  </PressableScale>
                </View>
              )}
            </View>
          ) : (
            <View style={{ marginTop: spacing.lg }}>
              <Input
                label="Offset from market"
                placeholder="0.00"
                keyboardType="numbers-and-punctuation"
                value={floatOffsetPctInput}
                onChangeText={setFloatOffsetPctInput}
                leftAdornment={
                  <Text variant="bodyMed" weight="700" color="textSecondary">
                    %
                  </Text>
                }
                helper={`Positive sells above market, negative below. Capped at ±${MAX_FLOAT_OFFSET_PCT}%.`}
              />
              {suggestedRate == null || rateStale ? (
                <View
                  style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.sm }}
                >
                  <Icon name="warning" size={14} color={colors.danger} />
                  <Text variant="caption" color="danger" style={{ flex: 1 }}>
                    Live rate unavailable — float ads can&apos;t be published right now.
                  </Text>
                </View>
              ) : (
                <View
                  style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.sm }}
                >
                  <Icon name="info" size={14} color={colors.accent} />
                  <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                    Anchor: Bybit P2P sell · Market: {symbol}{suggestedRate.toFixed(2)}
                    {effectiveRate != null && ` · Your rate: ${symbol}${effectiveRate.toFixed(2)}`}
                  </Text>
                </View>
              )}
              {!floatOffsetPctValid && floatOffsetPctInput.trim() !== '' && (
                <View
                  style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.xs }}
                >
                  <Icon name="warning" size={14} color={colors.danger} />
                  <Text variant="caption" color="danger" style={{ flex: 1 }}>
                    Enter a number within ±{MAX_FLOAT_OFFSET_PCT}%.
                  </Text>
                </View>
              )}
            </View>
          )}

          {/* ── Order limits ─────────────────────────────────────────── */}
          <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.lg }}>
            <View style={{ flex: 1 }}>
              <Input
                label={`Min order (${currencyCode})`}
                placeholder="Optional"
                keyboardType="decimal-pad"
                value={minLimitInput}
                onChangeText={setMinLimitInput}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Input
                label={`Max order (${currencyCode})`}
                placeholder="Optional"
                keyboardType="decimal-pad"
                value={maxLimitInput}
                onChangeText={setMaxLimitInput}
              />
            </View>
          </View>

          {/* ── Payment methods ──────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            {isPm ? 'Payment method' : 'Payment methods'}
          </Text>
          {isPm ? (
            // PM ads settle automatically, so no manual payment method is
            // selected or required. Under central inbound (payoutFees set)
            // buyers pay TSA Connect's account and the merchant's naira is
            // forwarded net of fees; otherwise buyers pay the merchant's own
            // virtual account.
            <View style={{ gap: spacing.sm }}>
              <Card padding="md" style={{ borderWidth: 1, borderColor: colors.accent, backgroundColor: colors.accentMuted }}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Icon name="checkCircle" size={22} color={colors.accent} />
                  <View style={{ flex: 1 }}>
                    <Text variant="bodyMed" weight="700">
                      {payoutFees ? 'Auto-settled through TSA Connect' : 'Auto-settled via your virtual account'}
                    </Text>
                    <Text variant="caption" color="textSecondary">
                      {payoutFees
                        ? 'Buyers pay TSA Connect’s account. Crypto releases automatically and your naira is sent to your settlement bank as each trade settles.'
                        : 'Buyers pay your virtual account and crypto releases automatically — no manual payment method needed.'}
                    </Text>
                  </View>
                </View>
              </Card>
              <PmPayoutFeeNotice fees={payoutFees} />
            </View>
          ) : loadingPms ? (
            <View style={{ paddingVertical: spacing.lg }}>
              <Spinner />
            </View>
          ) : paymentMethods.length === 0 ? (
            <PressableScale onPress={() => router.push('/p2p/payment-methods/new')}>
              <AccentCard padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Icon name="plus" size={20} color={colors.accent} />
                  <Text variant="bodyMed" weight="700" style={{ flex: 1 }}>
                    Add a payment method before publishing
                  </Text>
                  <Icon name="chevronRight" size={18} color={colors.textMuted} />
                </View>
              </AccentCard>
            </PressableScale>
          ) : (
            <View style={{ gap: spacing.sm }}>
              {paymentMethods.map((m) => {
                const selected = selectedPmIds.includes(m.id);
                return (
                  <PressableScale key={m.id} onPress={() => togglePm(m.id)}>
                    <Card
                      padding="md"
                      style={{
                        borderWidth: 1,
                        borderColor: selected ? colors.accent : colors.border,
                        backgroundColor: selected ? colors.accentMuted : colors.surface,
                      }}
                    >
                      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                        {selected ? (
                          <Icon name="checkCircle" size={22} color={colors.accent} />
                        ) : (
                          // Unselected = empty outline circle (a checkbox affordance),
                          // not a ✕ — an X reads as "remove/error", not "tap to select".
                          <View
                            style={{
                              width: 22,
                              height: 22,
                              borderRadius: 11,
                              borderWidth: 2,
                              borderColor: colors.border,
                            }}
                          />
                        )}
                        <View style={{ flex: 1 }}>
                          <Text variant="bodyMed" weight="600">
                            {m.bankName}
                          </Text>
                          <Text variant="caption" color="textSecondary">
                            {m.accountName} · {m.accountNumber.slice(-4)}
                          </Text>
                        </View>
                      </View>
                    </Card>
                  </PressableScale>
                );
              })}
            </View>
          )}

          {/* ── Preview ──────────────────────────────────────────────── */}
          <Card style={{ marginTop: spacing.xl }}>
            <Text variant="overline" color="textMuted">
              PREVIEW
            </Text>
            <Divider spacing="sm" />
            <SummaryCard>
              <SummaryRow label="Selling" value={`${amountInput || '0'} ${token.symbol}`} />
              <SummaryRow
                label="At"
                value={
                  <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
                    <Text variant="bodyMed" weight="600" numeric>
                      {symbol}{effectiveRate != null ? effectiveRate.toFixed(2) : '0'} / {token.symbol}
                    </Text>
                    {rateType === 'float' && (
                      <Text variant="caption" color="accent">
                        (auto)
                      </Text>
                    )}
                  </View>
                }
              />
              <SummaryRow
                label="Total value"
                value={`${symbol}${previewFiat.toLocaleString(undefined, { maximumFractionDigits: 2 })}`}
              />
              <SummaryRow
                label="Platform fee (1%)"
                value={`${((parseFloat(amountInput) || 0) * P2P_PLATFORM_FEE_PCT).toLocaleString(undefined, {
                  maximumFractionDigits: 4,
                })} ${token.symbol}`}
              />
            </SummaryCard>

            {/* Merchant fee disclosure — the seller (you) is charged 1% only on
                a successfully completed trade. Mirrors FeatureP2PPlatform (1%). */}
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'flex-start',
                gap: spacing.sm,
                marginTop: spacing.md,
                backgroundColor: colors.infoBg,
                borderRadius: radius.sm,
                padding: spacing.md,
              }}
            >
              <Icon name="info" size={16} color={colors.info} />
              <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                A 1% platform fee is charged to you (the merchant) only when a trade completes —
                on USDT and USDC alike. It is split instantly into the system fee, cashback
                to the buyer and their direct upline, and Trade Points.
              </Text>
            </View>
          </Card>

          {/* ── Publish ──────────────────────────────────────────────── */}
          <Button
            title="Publish ad"
            leftIcon="check"
            onPress={handleSubmit}
            disabled={!valid || submitting}
            loading={submitting}
            style={{ marginTop: spacing.xl }}
          />
          {submitting && !!submitStage && (
            <View style={{ alignItems: 'center', marginTop: spacing.sm }}>
              <Text variant="caption" color="textSecondary">
                {submitStage}
              </Text>
            </View>
          )}
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default CreateAdScreen;
