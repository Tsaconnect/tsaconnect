// app/p2p/create-ad.tsx
//
// Refactor of the legacy screens/buyP2P.tsx / sellP2P.tsx stubs. Wires the
// real backend:
//
//   1. POST /p2p/ads → returns { ad, unsignedTx }
//   2. signTransaction(unsignedTx) using the user's local wallet
//   3. broadcastTransaction → txHash
//   4. POST /p2p/ads/:id/confirm with txHash → ad goes active
//
// The unsigned tx is the seller's `createAd(adId, token, amount)` call;
// gas is pre-estimated server-side so the seller only needs to sign +
// broadcast.

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
import type { ChainKey } from '../../constants/chains';
import {
  adsApi,
  paymentMethodsApi,
  parseTokenAmount,
  type P2PPaymentMethod,
  type UnsignedTx,
} from '../../services/p2p';
import { getActiveWallet, signTransaction, broadcastTransaction } from '../../services/wallet';
import { exchangeRateService } from '../../services/exchangeRateApi';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
};

// Phase 1: USDT and MCGP on Sonic only. USDC will follow once mainnet
// liquidity exists.
const SUPPORTED_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'MCGP', decimals: 18 },
] as const;

const CreateAdScreen: React.FC = () => {
  const [token, setToken] = useState<(typeof SUPPORTED_TOKENS)[number]>(SUPPORTED_TOKENS[0]);
  const [amountInput, setAmountInput] = useState(''); // human decimal
  const [rateInput, setRateInput] = useState('');
  const [minLimitInput, setMinLimitInput] = useState('');
  const [maxLimitInput, setMaxLimitInput] = useState('');
  const [paymentMethods, setPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmIds, setSelectedPmIds] = useState<string[]>([]);
  const [loadingPms, setLoadingPms] = useState(true);
  const [suggestedRate, setSuggestedRate] = useState<number | null>(null);
  const [rateStale, setRateStale] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState<string>('');

  // Load payment methods + suggested Bybit rate concurrently on mount.
  useEffect(() => {
    paymentMethodsApi.list().then((res) => {
      if (res.success && res.data) {
        setPaymentMethods(res.data.filter((m) => m.currency === 'NGN'));
      }
      setLoadingPms(false);
    });
    exchangeRateService
      .getRates()
      .then((res) => {
        const ngn = res.rates?.NGN;
        if (ngn) {
          setSuggestedRate(ngn.binanceSell); // rate for sellers (user sells USDT → buyer pays NGN)
          setRateStale(!!ngn.stale);
        }
      })
      .catch(() => {});
  }, []);

  const togglePm = (id: string) => {
    setSelectedPmIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]
    );
  };

  // Derived totals for the live preview. Cheap to recompute on every input
  // change; floats are fine for display since the source-of-truth string
  // is what goes to the wire.
  const previewFiat = useMemo(() => {
    const amt = parseFloat(amountInput || '0');
    const rate = parseFloat(rateInput || '0');
    if (!isFinite(amt) || !isFinite(rate)) return 0;
    return amt * rate;
  }, [amountInput, rateInput]);

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    parseFloat(rateInput || '0') > 0 &&
    selectedPmIds.length > 0;

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

      // 2. Convert human inputs to base units / canonical strings.
      const tokenAmount = parseTokenAmount(amountInput, token.decimals);
      // Rate is fiat per token base unit. NUMERIC(38,10) on the server side,
      // so a literal decimal string is fine.
      const rate = rateInput.trim();
      const minLimit = minLimitInput.trim() ? minLimitInput.trim() : undefined;
      const maxLimit = maxLimitInput.trim() ? maxLimitInput.trim() : undefined;

      // 3. Create ad (prepare phase).
      setSubmitStage('Preparing escrow transaction…');
      const createRes = await adsApi.create({
        token: token.symbol,
        tokenAmount,
        fiatCurrency: 'NGN',
        rate,
        minLimit,
        maxLimit,
        paymentMethodIds: selectedPmIds,
        sellerAddress,
      });
      if (!createRes.success || !createRes.data) {
        throw new Error(createRes.message || 'Server rejected the ad');
      }
      const { ad, unsignedTx } = createRes.data;

      // 4. Sign the unsigned tx with the local wallet.
      setSubmitStage('Signing transaction…');
      const signed = await signTransaction(unsignedTxToTxLike(unsignedTx));

      // 5. Broadcast via the Sonic RPC.
      setSubmitStage('Broadcasting to Sonic…');
      const sonicChain = pickSonicChainKey(unsignedTx.chainId);
      const txResponse = await broadcastTransaction(sonicChain, signed);
      const txHash = txResponse.hash;

      // 6. Confirm with the backend — moves the ad pending → active.
      setSubmitStage('Confirming on-chain settlement…');
      const confirmRes = await adsApi.confirm(ad.id, txHash, sellerAddress);
      if (!confirmRes.success) {
        // The ad row exists (pending) and the tx broadcast happened. The
        // chain reconciler will eventually flip the ad to active. Surface
        // the error so the user understands they don't need to retry the
        // broadcast.
        throw new Error(
          (confirmRes.message || 'Confirm failed') +
            ' — the chain reconciler will catch up automatically.'
        );
      }

      Alert.alert('Ad published', `Your ad is now live in the marketplace.`, [
        // my-ads route lands in Step 16; until then bounce back to the
        // P2P landing.
        { text: 'OK', onPress: () => router.back() },
      ]);
    } catch (err: any) {
      Alert.alert('Could not publish ad', err?.message || String(err));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <Text style={styles.heading}>Create sell ad</Text>
          <Text style={styles.subheading}>
            You're offering {token.symbol} for NGN. Funds lock on chain when you publish;
            they unlock to buyers per trade or back to you on cancel.
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

          <Text style={styles.label}>Amount ({token.symbol})</Text>
          <View style={styles.inputCard}>
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
              <Text style={styles.previewValue}>₦{rateInput || '0'} / {token.symbol}</Text>
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

// pickSonicChainKey resolves the chain to use for broadcast. The frontend's
// chain config uses a single `sonic` key with the network (mainnet vs
// testnet) determined by useNetwork() — the RPC URLs swap accordingly, so
// we always return 'sonic' and let the wallet layer pick the right URL.
function pickSonicChainKey(_chainId: string | undefined): ChainKey {
  return 'sonic';
}

// unsignedTxToTxLike adapts the server's UnsignedTx (hex without 0x prefix)
// to ethers's TransactionLike shape (expects 0x prefixes on hex fields).
function unsignedTxToTxLike(tx: UnsignedTx) {
  const ensure0x = (v: string | undefined) => {
    if (!v) return undefined;
    return v.startsWith('0x') ? v : `0x${v}`;
  };
  return {
    to: tx.to,
    value: tx.value || '0',
    data: ensure0x(tx.data),
    nonce: tx.nonce,
    gasLimit: tx.gasLimit,
    gasPrice: tx.gasPrice,
    chainId: tx.chainId ? Number(tx.chainId) : undefined,
  };
}

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
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0 },
  inputPrefix: { fontSize: 16, fontWeight: '700', color: GOLD.dark, marginRight: 6 },
  inputSuffix: { fontSize: 14, fontWeight: '700', color: GOLD.dark, marginLeft: 6 },
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
