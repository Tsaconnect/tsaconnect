// app/p2p/create-ad.tsx
//
// Pool/voucher publish flow:
//
//   1. POST /p2p/ads → returns { ad (pending), typedData }
//   2. signTypedData(typedData) using the user's local wallet — off-chain,
//      no gas, no broadcast
//   3. POST /p2p/ads/:id/sign with the signature → ad goes active
//
// Capital is locked at the user-balance level via a separate deposit flow
// (see app/p2p/deposit.tsx). Publishing an ad never moves on-chain funds.
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// Visual/UX only — the EIP-712 voucher signing flow, field validation,
// payment-method selection, and fixed/float rate logic are unchanged.

import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  chainsApi,
  formatTokenAmount,
  paymentMethodsApi,
  poolApi,
  parseTokenAmount,
  type P2PChainInfo,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet, signTypedData, verifyWalletKey } from '../../services/wallet';
import { exchangeRateService } from '../../services/exchangeRateApi';
import { MAX_FLOAT_OFFSET_PCT, pctToBps } from '../../constants/p2p';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useCurrency } from '../../contexts/CurrencyContext';
import CurrencySelector from '../../components/currency/CurrencySelector';
import { getChainByChainId } from '../../constants/chains';
import { useTheme } from '@/theme';
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
// is unreachable so the screen never crashes with an empty token set.
const FALLBACK_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
  { symbol: 'MCGP', decimals: 18 },
];

type TokenEntry = { symbol: string; decimals: number };
type RateType = 'fixed' | 'float';

// Sonic chain IDs (mainnet + testnet). Used for the "Sonic-first" chain sort
// and the pool-balance caveat copy.
const SONIC_CHAIN_IDS = new Set([146, 14601]);

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
  const { colors, spacing } = useTheme();

  // Currency for THIS ad is a local form field. We seed it from the P2P market
  // currency (do not mutate the hook's setCurrency here), then resolve the full
  // config off supportedCurrencies so we have the right symbol for display.
  const { currency: marketCurrency } = useP2pCurrency();
  const { supportedCurrencies } = useCurrency();
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
  // Pool available balance for the selected token. Surfaced inline so the
  // seller can see whether their pool can back the ad before they sign.
  // `null` = not yet loaded; `'-'` = load failed (we don't block submit on
  // a transient pool-balance read failure — the backend re-checks).
  const [poolAvailable, setPoolAvailable] = useState<string | null>(null);

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
  const selectedChainConfig = selectedChainId != null
    ? getChainByChainId(selectedChainId)
    : undefined;

  // Show the chain chooser only when:
  //  (a) we have a valid backend response, AND
  //  (b) more than one chain offers the current token.
  const showChainChooser = chainsFetched && chainsForToken.length > 1;

  // Pool-balance caveat: shown when the selected chain is NOT Sonic, since the
  // pool balance endpoint is currently Sonic-scoped (Task 8 fixes this).
  const showPoolChainCaveat =
    selectedChainId != null && !isSonicChain(selectedChainId);

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

  // Refresh the pool balance whenever the token selection changes. Doesn't
  // block submit on failure — the backend's CreateAd does an authoritative
  // ReadBalance check anyway. This is a UX hint.
  useEffect(() => {
    let cancelled = false;
    setPoolAvailable(null);
    (async () => {
      const walletAddr = await getActiveWallet();
      if (cancelled) return;
      if (!walletAddr) {
        setPoolAvailable('-');
        return;
      }
      const res = await poolApi.balance(token.symbol, walletAddr);
      if (cancelled) return;
      if (res.success && res.data) {
        setPoolAvailable(res.data.available);
      } else {
        setPoolAvailable('-');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token.symbol]);

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

  // Block submit when the requested ad amount exceeds the seller's pool —
  // the backend would reject anyway with ErrInvalidParams. This is a
  // UX-side gate so we don't ask the user to sign a voucher we know is
  // going to fail. Unknown pool balance ("-") doesn't block (let the
  // server be authoritative).
  const exceedsPool = useMemo(() => {
    if (!poolAvailable || poolAvailable === '-') return false;
    if (!/^\d+(\.\d+)?$/.test(amountInput.trim())) return false;
    try {
      const want = BigInt(parseTokenAmount(amountInput.trim(), token.decimals));
      const have = BigInt(poolAvailable);
      return want > have;
    } catch {
      return false;
    }
  }, [amountInput, poolAvailable, token.decimals]);

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    !exceedsPool &&
    selectedPmIds.length > 0 &&
    (rateType === 'fixed'
      ? parseFloat(rateInput || '0') > 0
      : floatOffsetPctValid && floatSupported && suggestedRate != null && !rateStale);

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
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

      // 2. Convert human inputs to base units / canonical strings.
      const tokenAmount = parseTokenAmount(amountInput, token.decimals);
      const minLimit = minLimitInput.trim() ? minLimitInput.trim() : undefined;
      const maxLimit = maxLimitInput.trim() ? maxLimitInput.trim() : undefined;

      // 3. Create ad (prepare phase). Fixed mode sends rate; float mode
      // Step 1: ask the backend to create the ad in `pending` state. The
      // server stamps the voucher fields and returns an EIP-712 typed-data
      // payload for the seller to sign. No on-chain tx at publish time —
      // the only gas the seller pays is a one-off deposit (separate flow).
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

      // Step 2: sign the voucher off-chain via EIP-712. No gas, no broadcast.
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

      // Step 3: submit the signature back. Server verifies the recovered
      // signer matches `sellerAddress` and flips the ad to `active`.
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
      Alert.alert('Could not publish ad', err?.message || String(err));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
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

  const poolHint =
    poolAvailable && poolAvailable !== '-'
      ? `Pool: ${formatTokenAmount(poolAvailable, token.decimals, 4)} ${token.symbol}`
      : poolAvailable === '-'
        ? 'Pool unavailable'
        : undefined;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Create sell ad
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            You&apos;re offering {token.symbol} for {currencyCode}. Publishing is off-chain — you&apos;ll
            sign a voucher to authorise trades. Funds are drawn from your pool balance (deposit
            first via the Pool screen).
          </Text>

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
                {chainsForToken.map((c) => {
                  const cfg = getChainByChainId(c.chainId);
                  const label = cfg?.shortName ?? String(c.chainId);
                  return (
                    <Chip
                      key={c.chainId}
                      label={label}
                      selected={selectedChainId === c.chainId}
                      onPress={() => setSelectedChainId(c.chainId)}
                    />
                  );
                })}
              </View>
              {showPoolChainCaveat && selectedChainConfig && (
                <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.xs, marginTop: spacing.sm }}>
                  <Icon name="info" size={14} color={colors.textMuted} />
                  <Text variant="caption" color="textMuted" style={{ flex: 1 }}>
                    Pool balances are per-chain — fund your {selectedChainConfig.shortName} pool before publishing.
                  </Text>
                </View>
              )}
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
              onMax={
                poolAvailable && poolAvailable !== '-'
                  ? () =>
                      setAmountInput(
                        formatTokenAmount(poolAvailable!, token.decimals, token.decimals),
                      )
                  : undefined
              }
              maxLabel="Use pool"
              helper={exceedsPool ? undefined : poolHint}
              error={
                exceedsPool
                  ? `Exceeds your pool balance. Deposit more ${token.symbol} via the Pool screen.`
                  : undefined
              }
            />
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
            Payment methods
          </Text>
          {loadingPms ? (
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
                        <Icon
                          name={selected ? 'checkCircle' : 'closeCircle'}
                          size={22}
                          color={selected ? colors.accent : colors.textMuted}
                        />
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
                label="Fee on release"
                value={token.symbol === 'MCGP' ? '0% (MCGP)' : '0.5%'}
              />
            </SummaryCard>
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
