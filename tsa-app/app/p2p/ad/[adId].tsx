// app/p2p/ad/[adId].tsx
//
// Ad detail — shows the seller's terms and the user's fiat-amount entry to
// open a trade. On submit: prepare openTrade tx → sign → broadcast →
// confirm-open → navigate to trade detail.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
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
  paymentMethodsApi,
  tradesApi,
  formatFiat,
  formatTokenAmount,
  type P2PAd,
  type P2PPaymentMethod,
  type UnsignedTx,
} from '../../../services/p2p';
import {
  broadcastTransaction,
  getActiveWallet,
  signTransaction,
} from '../../../services/wallet';
import type { ChainKey } from '../../../constants/chains';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

const AdDetailScreen: React.FC = () => {
  const { adId } = useLocalSearchParams<{ adId: string }>();
  const [ad, setAd] = useState<P2PAd | null>(null);
  const [paymentMethods, setPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmId, setSelectedPmId] = useState<string>('');
  const [fiatInput, setFiatInput] = useState('');
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState('');

  const load = useCallback(async () => {
    if (!adId) return;
    const [adRes, pmRes] = await Promise.all([
      adsApi.get(String(adId)),
      paymentMethodsApi.list(),
    ]);
    if (adRes.success && adRes.data) setAd(adRes.data);
    if (pmRes.success && pmRes.data) {
      // Filter to methods that the AD allows. The seller picks which of
      // their methods can receive payments per ad.
      const allowed = pmRes.data.filter((m) =>
        adRes.data?.paymentMethodIds.includes(m.id)
      );
      setPaymentMethods(allowed);
      // Auto-select first if only one option.
      if (allowed.length === 1) setSelectedPmId(allowed[0].id);
    }
    setLoading(false);
  }, [adId]);

  useEffect(() => {
    load();
  }, [load]);

  const tokenDecimals = ad ? TOKEN_DECIMALS[ad.token] ?? 18 : 6;
  // Computed token amount the buyer would receive at the ad's rate.
  const tokenPreview = useMemo(() => {
    if (!ad) return '0';
    const fiat = parseFloat(fiatInput || '0');
    const rate = parseFloat(ad.rate);
    if (!isFinite(fiat) || !isFinite(rate) || rate <= 0) return '0';
    return (fiat / rate).toFixed(Math.min(tokenDecimals, 4));
  }, [ad, fiatInput, tokenDecimals]);

  const valid =
    !!ad &&
    !ad.rateStale &&
    parseFloat(fiatInput || '0') > 0 &&
    !!selectedPmId;

  const handleOpen = async () => {
    if (!valid || !ad || submitting) return;
    setSubmitting(true);
    try {
      setSubmitStage('Loading wallet…');
      const buyerAddress = await getActiveWallet();
      if (!buyerAddress) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      setSubmitStage('Preparing openTrade transaction…');
      const openRes = await tradesApi.open(ad.id, {
        fiatAmount: fiatInput.trim(),
        paymentMethodId: selectedPmId,
        buyerAddress,
      });
      // 503 means the rate feed went stale between page-load and this click.
      // Surface a clean message and refresh the ad so the stale badge appears.
      if (openRes.status === 503) {
        Alert.alert(
          'Live rate unavailable',
          "The seller's rate feed is temporarily down. Please try again in a few seconds — the ad will reactivate automatically once the feed recovers.",
        );
        load();
        return;
      }
      if (!openRes.success || !openRes.data) {
        throw new Error(openRes.message || 'Server rejected the trade');
      }
      const { trade, unsignedTx } = openRes.data;

      setSubmitStage('Signing transaction…');
      const signed = await signTransaction(toTxLike(unsignedTx), buyerAddress);

      setSubmitStage('Broadcasting to Sonic…');
      const txResp = await broadcastTransaction('sonic' as ChainKey, signed);
      const txHash = txResp.hash;

      setSubmitStage('Confirming on chain…');
      const confirmRes = await tradesApi.confirmOpen(trade.id, txHash, buyerAddress);
      if (!confirmRes.success) {
        throw new Error(
          (confirmRes.message || 'Confirm failed') +
            ' — the chain reconciler will catch up automatically.'
        );
      }

      router.replace(`/p2p/trade/${trade.id}`);
    } catch (err: any) {
      Alert.alert('Could not open trade', err?.message || String(err));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  if (loading || !ad) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }

  const minOk =
    !ad.minLimit || parseFloat(fiatInput || '0') >= parseFloat(ad.minLimit || '0');
  const maxOk =
    !ad.maxLimit || parseFloat(fiatInput || '0') <= parseFloat(ad.maxLimit || '0');

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          {(() => {
            const sellerLabel =
              ad.sellerUsername || `seller-${ad.sellerId.slice(0, 4)}`;
            const sellerInitial = sellerLabel.charAt(0).toUpperCase();
            return (
              <View style={styles.sellerHeader}>
                <View style={styles.sellerAvatar}>
                  <Text style={styles.sellerAvatarText}>{sellerInitial}</Text>
                </View>
                <View style={{ flex: 1 }}>
                  <Text style={styles.sellerName} numberOfLines={1}>
                    @{sellerLabel}
                  </Text>
                  <Text style={styles.sellerSub}>Selling {ad.token} for NGN</Text>
                </View>
                {ad.rateType === 'float' && (
                  <View style={styles.floatChipRow}>
                    <Icon name="trending-up" size={11} color={GOLD.dark} />
                    <Text style={styles.floatChipText}>Float</Text>
                  </View>
                )}
              </View>
            );
          })()}

          <View style={styles.summary}>
            <Text style={styles.summaryLabel}>Price</Text>
            <View style={styles.summaryRateRow}>
              <Text style={styles.summaryCurrency}>NGN</Text>
              <Text style={styles.summaryRate}>{formatFiat(ad.rate)}</Text>
              <Text style={styles.summaryRateUnit}>/ {ad.token}</Text>
            </View>
            {ad.rateType === 'float' && (
              <Text style={styles.floatHint}>
                Float rate — recomputes from the live market when you open a trade.
              </Text>
            )}
            {ad.rateStale && (
              <View style={styles.staleBanner}>
                <Icon name="error-outline" size={15} color={GOLD.error} />
                <Text style={styles.staleBannerText}>
                  Live rate unavailable. Trades are temporarily disabled —
                  the ad will reactivate automatically once the feed recovers.
                </Text>
              </View>
            )}

            <View style={styles.summaryDivider} />

            <View style={styles.summaryMetaGrid}>
              <View style={styles.summaryMetaCol}>
                <Text style={styles.summaryMetaLabel}>Available</Text>
                <Text style={styles.summaryMetaValue}>
                  {formatTokenAmount(ad.remaining, tokenDecimals, 4)} {ad.token}
                </Text>
              </View>
              {ad.minLimit && ad.maxLimit && (
                <View style={[styles.summaryMetaCol, styles.summaryMetaColRight]}>
                  <Text style={styles.summaryMetaLabel}>Order range</Text>
                  <Text style={styles.summaryMetaValue}>
                    ₦{formatFiat(ad.minLimit, 0)} – ₦{formatFiat(ad.maxLimit, 0)}
                  </Text>
                </View>
              )}
            </View>
          </View>

          {ad.terms && (
            <View style={styles.terms}>
              <View style={styles.termsHeader}>
                <Icon name="info-outline" size={14} color={GOLD.dark} />
                <Text style={styles.termsLabel}>Seller's terms</Text>
              </View>
              <Text style={styles.termsBody}>{ad.terms}</Text>
            </View>
          )}

          <Text style={styles.label}>You pay (NGN)</Text>
          <View style={styles.inputCard}>
            <Text style={styles.inputPrefix}>₦</Text>
            <TextInput
              style={styles.inputField}
              placeholder="0"
              placeholderTextColor="#999"
              keyboardType="decimal-pad"
              value={fiatInput}
              onChangeText={setFiatInput}
            />
          </View>
          {!minOk && fiatInput && (
            <Text style={styles.errorText}>Below seller's minimum (₦{ad.minLimit})</Text>
          )}
          {!maxOk && fiatInput && (
            <Text style={styles.errorText}>Above seller's maximum (₦{ad.maxLimit})</Text>
          )}

          <View style={styles.preview}>
            <Text style={styles.previewLabel}>You receive</Text>
            <Text style={styles.previewValue}>
              {tokenPreview} {ad.token}
            </Text>
          </View>

          <Text style={styles.label}>Pay via</Text>
          {paymentMethods.length === 0 ? (
            <Text style={styles.placeholder}>Seller has no compatible payment methods configured.</Text>
          ) : (
            paymentMethods.map((m) => (
              <TouchableOpacity
                key={m.id}
                style={[styles.pmCard, selectedPmId === m.id && styles.pmCardActive]}
                onPress={() => setSelectedPmId(m.id)}
                activeOpacity={0.8}
              >
                <Icon
                  name={selectedPmId === m.id ? 'radio-button-checked' : 'radio-button-unchecked'}
                  size={22}
                  color={selectedPmId === m.id ? GOLD.dark : '#999'}
                />
                <View style={{ flex: 1, marginLeft: 10 }}>
                  <Text style={styles.pmBank}>{m.bankName}</Text>
                  <Text style={styles.pmAccount}>
                    {m.accountName} · ••••{m.accountNumber.slice(-4)}
                  </Text>
                </View>
              </TouchableOpacity>
            ))
          )}

          <TouchableOpacity
            style={[
              styles.submit,
              (!valid || !minOk || !maxOk || submitting) && styles.submitDisabled,
            ]}
            onPress={handleOpen}
            disabled={!valid || !minOk || !maxOk || submitting}
            activeOpacity={0.85}
          >
            {submitting ? (
              <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                <ActivityIndicator color="#000" />
                <Text style={[styles.submitText, { marginLeft: 10 }]}>{submitStage}</Text>
              </View>
            ) : (
              <Text style={styles.submitText}>Open trade</Text>
            )}
          </TouchableOpacity>

          <Text style={styles.disclaimer}>
            After opening, you have 30 minutes to send fiat. The seller releases your
            {' '}{ad.token} after confirming receipt.
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

function toTxLike(tx: UnsignedTx) {
  const ensure0x = (v: string | undefined) =>
    !v ? undefined : v.startsWith('0x') ? v : `0x${v}`;
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
  scroll: { padding: 20, paddingBottom: 80 },
  sellerHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    backgroundColor: '#FFF',
    padding: 14,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: GOLD.muted,
    marginBottom: 12,
  },
  sellerAvatar: {
    width: 42,
    height: 42,
    borderRadius: 21,
    backgroundColor: GOLD.light,
    borderWidth: 1,
    borderColor: GOLD.muted,
    alignItems: 'center',
    justifyContent: 'center',
  },
  sellerAvatarText: { fontSize: 17, fontWeight: '800', color: GOLD.dark },
  sellerName: { fontSize: 15, fontWeight: '800', color: '#1A1A1A' },
  sellerSub: { fontSize: 12, color: '#888', marginTop: 2, fontWeight: '500' },
  summary: {
    backgroundColor: '#FFF',
    padding: 18,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: GOLD.muted,
    marginBottom: 16,
  },
  summaryLabel: {
    fontSize: 11,
    color: '#999',
    fontWeight: '700',
    letterSpacing: 0.4,
    textTransform: 'uppercase',
    marginBottom: 6,
  },
  summaryRateRow: { flexDirection: 'row', alignItems: 'baseline', gap: 6 },
  summaryCurrency: { fontSize: 13, fontWeight: '800', color: GOLD.dark, letterSpacing: 0.5 },
  summaryRate: { fontSize: 30, fontWeight: '800', color: '#000', letterSpacing: -0.4 },
  summaryRateUnit: { fontSize: 14, color: '#666', fontWeight: '600' },
  floatHint: { fontSize: 12, color: '#777', marginTop: 8, lineHeight: 17 },
  floatChipRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
    backgroundColor: GOLD.light,
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 6,
  },
  floatChipText: { fontSize: 11, color: GOLD.dark, fontWeight: '700' },
  staleBanner: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: 6,
    marginTop: 10,
    backgroundColor: '#FDECEF',
    padding: 10,
    borderRadius: 8,
  },
  staleBannerText: { flex: 1, fontSize: 12, color: GOLD.error, lineHeight: 17 },
  summaryDivider: { height: 1, backgroundColor: '#F1ECDE', marginTop: 14, marginBottom: 12 },
  summaryMetaGrid: { flexDirection: 'row', justifyContent: 'space-between' },
  summaryMetaCol: { flex: 1 },
  summaryMetaColRight: { alignItems: 'flex-end' },
  summaryMetaLabel: {
    fontSize: 11,
    color: '#999',
    fontWeight: '600',
    textTransform: 'uppercase',
    letterSpacing: 0.4,
    marginBottom: 3,
  },
  summaryMetaValue: { fontSize: 13, color: '#1A1A1A', fontWeight: '700' },
  terms: {
    backgroundColor: GOLD.light,
    padding: 14,
    borderRadius: 10,
    marginBottom: 16,
  },
  termsHeader: { flexDirection: 'row', alignItems: 'center', gap: 6, marginBottom: 6 },
  termsLabel: { fontSize: 12, fontWeight: '700', color: GOLD.dark, letterSpacing: 0.3 },
  termsBody: { fontSize: 13, color: '#333', lineHeight: 18 },
  label: { fontSize: 14, fontWeight: '700', color: '#333', marginTop: 8, marginBottom: 6 },
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
  inputField: { flex: 1, fontSize: 18, color: '#000', paddingVertical: 0, fontWeight: '600' },
  inputPrefix: { fontSize: 18, fontWeight: '700', color: GOLD.dark, marginRight: 6 },
  errorText: { color: GOLD.error, fontSize: 12, marginTop: 6 },
  preview: {
    backgroundColor: GOLD.light,
    padding: 14,
    borderRadius: 10,
    marginTop: 12,
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  previewLabel: { fontSize: 13, color: GOLD.dark, fontWeight: '600' },
  previewValue: { fontSize: 18, fontWeight: '800', color: '#000' },
  placeholder: { fontSize: 13, color: '#888', paddingVertical: 12, fontStyle: 'italic' },
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
  disclaimer: { fontSize: 12, color: '#888', marginTop: 12, textAlign: 'center', lineHeight: 16 },
});

export default AdDetailScreen;
