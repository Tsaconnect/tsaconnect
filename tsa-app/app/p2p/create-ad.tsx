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

import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  formatTokenAmount,
  paymentMethodsApi,
  poolApi,
  parseTokenAmount,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet, signTypedData, verifyWalletKey } from '../../services/wallet';
import { exchangeRateService } from '../../services/exchangeRateApi';
import { MAX_FLOAT_OFFSET_PCT, P2P_GOLD, pctToBps } from '../../constants/p2p';

const GOLD = P2P_GOLD;

const SUPPORTED_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
  { symbol: 'MCGP', decimals: 18 },
] as const;

type RateType = 'fixed' | 'float';

const CreateAdScreen: React.FC = () => {
  const [token, setToken] = useState<(typeof SUPPORTED_TOKENS)[number]>(SUPPORTED_TOKENS[0]);
  const [amountInput, setAmountInput] = useState(''); // human decimal
  const [rateType, setRateType] = useState<RateType>('fixed');
  const [rateInput, setRateInput] = useState('');
  // Signed decimal percent, e.g. "0.5" = +0.5%, "-1.25" = -1.25%. Converted
  // to basis points on submit. Only consulted when rateType==='float'.
  const [floatOffsetPctInput, setFloatOffsetPctInput] = useState('');
  const [minLimitInput, setMinLimitInput] = useState('');
  const [maxLimitInput, setMaxLimitInput] = useState('');
  const [paymentMethods, setPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmIds, setSelectedPmIds] = useState<string[]>([]);
  const [loadingPms, setLoadingPms] = useState(true);
  const [suggestedRate, setSuggestedRate] = useState<number | null>(null);
  const [rateStale, setRateStale] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState<string>('');
  // Pool available balance for the selected token. Surfaced inline so the
  // seller can see whether their pool can back the ad before they sign.
  // `null` = not yet loaded; `'-'` = load failed (we don't block submit on
  // a transient pool-balance read failure — the backend re-checks).
  const [poolAvailable, setPoolAvailable] = useState<string | null>(null);

  // Load payment methods + suggested Bybit rate concurrently on mount.
  // Both promises always clear their loading flag — a network reject must
  // not leave the PM picker spinning forever or the rate panel stuck.
  useEffect(() => {
    (async () => {
      try {
        const res = await paymentMethodsApi.list();
        if (res.success && res.data) {
          setPaymentMethods(res.data.filter((m) => m.currency === 'NGN'));
        }
      } catch (err) {
        console.warn('[create-ad] paymentMethodsApi.list failed', err);
      } finally {
        setLoadingPms(false);
      }
    })();
    (async () => {
      try {
        const res = await exchangeRateService.getRates();
        const ngn = res.rates?.NGN;
        if (ngn) {
          setSuggestedRate(ngn.binanceSell); // rate for sellers (user sells USDT → buyer pays NGN)
          setRateStale(!!ngn.stale);
        }
      } catch (err) {
        // Float-mode users will see "Live rate unavailable" — that's fine
        // for a feed outage; just log so post-mortem has a breadcrumb.
        console.warn('[create-ad] exchangeRateService.getRates failed', err);
      }
    })();
  }, []);

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

  const togglePm = (id: string) => {
    setSelectedPmIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]
    );
  };

  // Effective rate previewed to the seller. Fixed mode: the literal NGN
  // input. Float mode: anchor × (1 + offset%/100), null if the feed is
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
      : floatOffsetPctValid && suggestedRate != null && !rateStale);

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
        fiatCurrency: 'NGN',
        minLimit,
        maxLimit,
        paymentMethodIds: selectedPmIds,
        sellerAddress,
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

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <Text style={styles.heading}>Create sell ad</Text>
          <Text style={styles.subheading}>
            You're offering {token.symbol} for NGN. Publishing is off-chain — you'll
            sign a voucher to authorise trades. Funds are drawn from your pool
            balance (deposit first via the Pool screen).
          </Text>

          <Text style={styles.label}>Token</Text>
          <View style={styles.tokenRow}>
            {SUPPORTED_TOKENS.map((t) => (
              <TouchableOpacity
                key={t.symbol}
                style={[styles.tokenChip, token.symbol === t.symbol && styles.tokenChipActive]}
                onPress={() => setToken(t)}
                activeOpacity={0.8}
              >
                <Text
                  style={[
                    styles.tokenChipText,
                    token.symbol === t.symbol && styles.tokenChipTextActive,
                  ]}
                >
                  {t.symbol}
                </Text>
              </TouchableOpacity>
            ))}
          </View>

          <View style={styles.amountHeaderRow}>
            <Text style={styles.labelInline}>Amount ({token.symbol})</Text>
            {poolAvailable && poolAvailable !== '-' && (
              <TouchableOpacity
                onPress={() =>
                  setAmountInput(
                    formatTokenAmount(poolAvailable!, token.decimals, token.decimals),
                  )
                }
                hitSlop={6}
              >
                <Text style={styles.balanceHint}>
                  Pool: {formatTokenAmount(poolAvailable, token.decimals, 4)}{' '}
                  {token.symbol}
                </Text>
              </TouchableOpacity>
            )}
            {poolAvailable === '-' && (
              <Text style={styles.balanceHintMuted}>Pool unavailable</Text>
            )}
          </View>
          <View style={[styles.inputCard, exceedsPool && styles.inputCardError]}>
            <TextInput
              style={styles.inputField}
              placeholder="0.00"
              placeholderTextColor="#999"
              keyboardType="decimal-pad"
              value={amountInput}
              onChangeText={setAmountInput}
            />
            <Text style={styles.inputSuffix}>{token.symbol}</Text>
          </View>
          {exceedsPool && (
            <Text style={styles.amountError}>
              Exceeds your pool balance. Deposit more {token.symbol} via the Pool screen.
            </Text>
          )}

          <Text style={styles.label}>Rate type</Text>
          <View style={styles.segmentedRow}>
            <TouchableOpacity
              style={[styles.segmentBtn, rateType === 'fixed' && styles.segmentBtnActive]}
              onPress={() => setRateType('fixed')}
              activeOpacity={0.8}
            >
              <Text style={[styles.segmentText, rateType === 'fixed' && styles.segmentTextActive]}>
                Fixed
              </Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.segmentBtn, rateType === 'float' && styles.segmentBtnActive]}
              onPress={() => setRateType('float')}
              activeOpacity={0.8}
            >
              <Text style={[styles.segmentText, rateType === 'float' && styles.segmentTextActive]}>
                Float (auto-update)
              </Text>
            </TouchableOpacity>
          </View>

          {rateType === 'fixed' ? (
            <>
              <Text style={styles.label}>Rate (NGN per 1 {token.symbol})</Text>
              <View style={styles.inputCard}>
                <Text style={styles.inputPrefix}>₦</Text>
                <TextInput
                  style={styles.inputField}
                  placeholder={suggestedRate ? String(suggestedRate) : '1500'}
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                  value={rateInput}
                  onChangeText={setRateInput}
                />
              </View>
              {suggestedRate !== null && (
                <View style={styles.rateHint}>
                  <Icon name="info-outline" size={14} color={rateStale ? GOLD.error : GOLD.dark} />
                  <Text style={[styles.rateHintText, rateStale && { color: GOLD.error }]}>
                    {rateStale
                      ? `Bybit rate stale — verify before publishing`
                      : `Current Bybit P2P sell rate: ₦${suggestedRate.toFixed(2)}`}
                  </Text>
                  <TouchableOpacity onPress={() => setRateInput(String(suggestedRate))}>
                    <Text style={styles.rateApply}>Use</Text>
                  </TouchableOpacity>
                </View>
              )}
            </>
          ) : (
            <>
              <Text style={styles.label}>Offset from market</Text>
              <View style={styles.inputCard}>
                <Text style={styles.inputPrefix}>%</Text>
                <TextInput
                  style={styles.inputField}
                  placeholder="0.00"
                  placeholderTextColor="#999"
                  keyboardType="numbers-and-punctuation"
                  value={floatOffsetPctInput}
                  onChangeText={setFloatOffsetPctInput}
                />
              </View>
              <Text style={styles.helperText}>
                Positive sells above market, negative below. Capped at ±{MAX_FLOAT_OFFSET_PCT}%.
              </Text>
              {suggestedRate == null || rateStale ? (
                <View style={[styles.rateHint, { marginTop: 8 }]}>
                  <Icon name="error-outline" size={14} color={GOLD.error} />
                  <Text style={[styles.rateHintText, { color: GOLD.error }]}>
                    Live rate unavailable — float ads can't be published right now.
                  </Text>
                </View>
              ) : (
                <View style={[styles.rateHint, { marginTop: 8 }]}>
                  <Icon name="info-outline" size={14} color={GOLD.dark} />
                  <Text style={styles.rateHintText}>
                    Anchor: Bybit P2P sell · Market: ₦{suggestedRate.toFixed(2)}
                    {effectiveRate != null && (
                      <Text> · Your rate: ₦{effectiveRate.toFixed(2)}</Text>
                    )}
                  </Text>
                </View>
              )}
              {!floatOffsetPctValid && floatOffsetPctInput.trim() !== '' && (
                <View style={[styles.rateHint, { marginTop: 4 }]}>
                  <Icon name="warning" size={14} color={GOLD.error} />
                  <Text style={[styles.rateHintText, { color: GOLD.error }]}>
                    Enter a number within ±{MAX_FLOAT_OFFSET_PCT}%.
                  </Text>
                </View>
              )}
            </>
          )}

          <View style={styles.limitsRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.label}>Min order (NGN)</Text>
              <View style={styles.inputCard}>
                <TextInput
                  style={styles.inputField}
                  placeholder="Optional"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                  value={minLimitInput}
                  onChangeText={setMinLimitInput}
                />
              </View>
            </View>
            <View style={{ width: 12 }} />
            <View style={{ flex: 1 }}>
              <Text style={styles.label}>Max order (NGN)</Text>
              <View style={styles.inputCard}>
                <TextInput
                  style={styles.inputField}
                  placeholder="Optional"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                  value={maxLimitInput}
                  onChangeText={setMaxLimitInput}
                />
              </View>
            </View>
          </View>

          <Text style={styles.label}>Payment methods</Text>
          {loadingPms ? (
            <ActivityIndicator color={GOLD.dark} style={{ marginVertical: 16 }} />
          ) : paymentMethods.length === 0 ? (
            <TouchableOpacity
              style={styles.emptyPmCard}
              onPress={() => router.push('/p2p/payment-methods/new')}
              activeOpacity={0.8}
            >
              <Icon name="add-circle-outline" size={20} color={GOLD.dark} />
              <Text style={styles.emptyPmText}>
                Add a payment method before publishing
              </Text>
            </TouchableOpacity>
          ) : (
            paymentMethods.map((m) => (
              <TouchableOpacity
                key={m.id}
                style={[
                  styles.pmCard,
                  selectedPmIds.includes(m.id) && styles.pmCardActive,
                ]}
                onPress={() => togglePm(m.id)}
                activeOpacity={0.8}
              >
                <Icon
                  name={
                    selectedPmIds.includes(m.id)
                      ? 'check-box'
                      : 'check-box-outline-blank'
                  }
                  size={22}
                  color={selectedPmIds.includes(m.id) ? GOLD.dark : '#999'}
                />
                <View style={{ flex: 1, marginLeft: 10 }}>
                  <Text style={styles.pmBank}>{m.bankName}</Text>
                  <Text style={styles.pmAccount}>
                    {m.accountName} · {m.accountNumber.slice(-4)}
                  </Text>
                </View>
              </TouchableOpacity>
            ))
          )}

          <View style={styles.preview}>
            <Text style={styles.previewTitle}>Preview</Text>
            <View style={styles.previewRow}>
              <Text style={styles.previewLabel}>Selling</Text>
              <Text style={styles.previewValue}>
                {amountInput || '0'} {token.symbol}
              </Text>
            </View>
            <View style={styles.previewRow}>
              <Text style={styles.previewLabel}>At</Text>
              <Text style={styles.previewValue}>
                ₦{effectiveRate != null ? effectiveRate.toFixed(2) : '0'} / {token.symbol}
                {rateType === 'float' && <Text style={styles.previewMeta}> (auto)</Text>}
              </Text>
            </View>
            <View style={styles.previewRow}>
              <Text style={styles.previewLabel}>Total value</Text>
              <Text style={styles.previewValue}>
                ₦{previewFiat.toLocaleString(undefined, { maximumFractionDigits: 2 })}
              </Text>
            </View>
            <View style={styles.previewRow}>
              <Text style={styles.previewLabel}>Fee on release</Text>
              <Text style={styles.previewValue}>
                {token.symbol === 'MCGP' ? '0% (MCGP)' : '0.5%'}
              </Text>
            </View>
          </View>

          <TouchableOpacity
            style={[styles.submit, (!valid || submitting) && styles.submitDisabled]}
            onPress={handleSubmit}
            disabled={!valid || submitting}
            activeOpacity={0.85}
          >
            {submitting ? (
              <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                <ActivityIndicator color="#000" />
                <Text style={[styles.submitText, { marginLeft: 10 }]}>{submitStage}</Text>
              </View>
            ) : (
              <Text style={styles.submitText}>Publish ad</Text>
            )}
          </TouchableOpacity>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 6 },
  subheading: { fontSize: 14, color: '#666', marginBottom: 18, lineHeight: 20 },
  label: { fontSize: 14, fontWeight: '700', color: '#333', marginTop: 14, marginBottom: 6 },
  tokenRow: { flexDirection: 'row', gap: 10 },
  tokenChip: {
    paddingVertical: 10,
    paddingHorizontal: 18,
    borderRadius: 10,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  tokenChipActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  tokenChipText: { fontWeight: '700', color: GOLD.dark },
  tokenChipTextActive: { color: '#000' },
  inputCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 12,
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  inputCardError: { borderColor: GOLD.error },
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0 },
  inputPrefix: { fontSize: 16, fontWeight: '700', color: GOLD.dark, marginRight: 6 },
  inputSuffix: { fontSize: 14, fontWeight: '700', color: GOLD.dark, marginLeft: 6 },
  amountHeaderRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'baseline',
    marginTop: 14,
    marginBottom: 6,
  },
  // Inline label sized to match `label` but without its outer marginTop —
  // the parent amountHeaderRow already supplies the spacing.
  labelInline: { fontSize: 14, fontWeight: '700', color: '#333' },
  balanceHint: { fontSize: 12, color: GOLD.dark, fontWeight: '600' },
  balanceHintMuted: { fontSize: 12, color: '#999', fontWeight: '500' },
  amountError: { color: GOLD.error, fontSize: 12, marginTop: 6 },
  rateHint: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 6,
    paddingHorizontal: 4,
  },
  rateHintText: { flex: 1, marginLeft: 6, fontSize: 12, color: GOLD.dark },
  rateApply: { fontSize: 12, fontWeight: '700', color: GOLD.dark, textDecorationLine: 'underline' },
  limitsRow: { flexDirection: 'row' },
  pmCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    borderRadius: 10,
    padding: 12,
    marginBottom: 8,
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  pmCardActive: { borderColor: GOLD.dark, backgroundColor: GOLD.light },
  pmBank: { fontSize: 15, fontWeight: '600', color: '#000' },
  pmAccount: { fontSize: 12, color: '#666', marginTop: 2 },
  emptyPmCard: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#FFF',
    padding: 16,
    borderRadius: 10,
    borderWidth: 1,
    borderStyle: 'dashed',
    borderColor: GOLD.muted,
  },
  emptyPmText: { marginLeft: 8, fontWeight: '600', color: GOLD.dark },
  preview: {
    backgroundColor: GOLD.light,
    padding: 16,
    borderRadius: 12,
    marginTop: 20,
  },
  previewTitle: { fontSize: 14, fontWeight: '800', color: '#000', marginBottom: 10 },
  previewRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 6,
  },
  previewLabel: { fontSize: 13, color: '#666' },
  previewValue: { fontSize: 14, fontWeight: '700', color: '#000' },
  previewMeta: { fontSize: 11, fontWeight: '600', color: GOLD.dark },
  segmentedRow: {
    flexDirection: 'row',
    backgroundColor: '#FFF',
    borderRadius: 10,
    borderWidth: 1,
    borderColor: GOLD.muted,
    overflow: 'hidden',
  },
  segmentBtn: {
    flex: 1,
    paddingVertical: 12,
    alignItems: 'center',
    backgroundColor: '#FFF',
  },
  segmentBtnActive: { backgroundColor: GOLD.primary },
  segmentText: { fontSize: 14, fontWeight: '700', color: GOLD.dark },
  segmentTextActive: { color: '#000' },
  helperText: { marginTop: 6, fontSize: 12, color: '#666', paddingHorizontal: 4 },
  submit: {
    marginTop: 24,
    backgroundColor: GOLD.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    shadowColor: GOLD.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.25,
    shadowRadius: 6,
    elevation: 4,
  },
  submitDisabled: { opacity: 0.5 },
  submitText: { fontWeight: '800', color: '#000', fontSize: 16 },
});

export default CreateAdScreen;
