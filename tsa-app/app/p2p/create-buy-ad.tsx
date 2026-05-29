// app/p2p/create-buy-ad.tsx
//
// Merchant posts a buy-ad: they want to BUY crypto from users and will pay
// fiat. Different capital model from create-ad.tsx:
//
//   - No pool deposit required — the ad is a binding intent to buy.
//   - No EIP-712 voucher to sign — just create and it goes active.
//   - The `amount` field specifies the total crypto the merchant wants to buy.
//
// Flow:
//   1. Fill form (token, fiatCurrency, rate, limits, payment methods, amount)
//   2. POST /p2p/ads  { ...fields, side: 'buy', sellerAddress: merchantWallet }
//   3. Navigate back to marketplace (buy tab)

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
  paymentMethodsApi,
  parseTokenAmount,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet } from '../../services/wallet';
import { P2P_GOLD } from '../../constants/p2p';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useCurrency } from '../../contexts/CurrencyContext';
import { useUserRole } from '../../hooks/useUserRole';
import CurrencySelector from '../../components/currency/CurrencySelector';

const GOLD = P2P_GOLD;

const SUPPORTED_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
  { symbol: 'MCGP', decimals: 18 },
] as const;

const CreateBuyAdScreen: React.FC = () => {
  const { isMerchant, loading: roleLoading } = useUserRole();
  const { currency: marketCurrency } = useP2pCurrency();
  const { supportedCurrencies } = useCurrency();

  const [adCurrencyCode, setAdCurrencyCode] = useState<string | null>(null);
  const currencyCode = (adCurrencyCode ?? marketCurrency.code).toUpperCase();
  const currencyConfig =
    supportedCurrencies.find((c) => c.code === currencyCode) ?? marketCurrency;
  const symbol = currencyConfig.symbol;

  const [token, setToken] = useState<(typeof SUPPORTED_TOKENS)[number]>(SUPPORTED_TOKENS[0]);
  const [amountInput, setAmountInput] = useState(''); // human decimal — total to buy
  const [rateInput, setRateInput] = useState('');
  const [minLimitInput, setMinLimitInput] = useState('');
  const [maxLimitInput, setMaxLimitInput] = useState('');
  const [termsInput, setTermsInput] = useState('');
  const [allPaymentMethods, setAllPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmIds, setSelectedPmIds] = useState<string[]>([]);
  const [loadingPms, setLoadingPms] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState('');

  useEffect(() => {
    (async () => {
      try {
        const res = await paymentMethodsApi.list();
        if (res.success && res.data) setAllPaymentMethods(res.data);
      } catch (err) {
        console.warn('[create-buy-ad] paymentMethodsApi.list failed', err);
      } finally {
        setLoadingPms(false);
      }
    })();
  }, []);

  // Clear PM selection when currency changes.
  useEffect(() => {
    setSelectedPmIds([]);
  }, [currencyCode]);

  // Payment methods for the selected fiat currency.
  const paymentMethods = useMemo(
    () => allPaymentMethods.filter((m) => m.currency?.toUpperCase() === currencyCode),
    [allPaymentMethods, currencyCode],
  );

  const togglePm = (id: string) => {
    setSelectedPmIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  };

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    parseFloat(rateInput || '0') > 0 &&
    selectedPmIds.length > 0;

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
    try {
      setSubmitStage('Loading wallet…');
      const merchantWallet = await getActiveWallet();
      if (!merchantWallet) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      let tokenAmount: string;
      try {
        tokenAmount = parseTokenAmount(amountInput.trim(), token.decimals);
      } catch (e: any) {
        throw new Error(`Invalid amount: ${e?.message}`);
      }

      const minLimit = minLimitInput.trim() || undefined;
      const maxLimit = maxLimitInput.trim() || undefined;
      const terms = termsInput.trim() || undefined;

      setSubmitStage('Publishing buy ad…');
      const createRes = await adsApi.create({
        token: token.symbol,
        tokenAmount,
        fiatCurrency: currencyCode,
        rate: rateInput.trim(),
        rateType: 'fixed',
        minLimit,
        maxLimit,
        paymentMethodIds: selectedPmIds,
        sellerAddress: merchantWallet,
        side: 'buy',
        terms,
      });

      if (!createRes.success || !createRes.data) {
        throw new Error(createRes.message || 'Server rejected the buy ad');
      }

      // Buy-ads don't require voucher signing — navigate directly to my-ads.
      Alert.alert('Buy ad published', 'Your buy ad is now live. Users can respond to it.', [
        {
          text: 'OK',
          onPress: () => router.replace('/p2p/my-ads'),
        },
      ]);
    } catch (err: any) {
      Alert.alert('Could not create buy ad', err?.message || String(err));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  // Gate behind merchant role.
  if (roleLoading) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }

  if (!isMerchant) {
    return (
      <SafeAreaView style={styles.container}>
        <View style={styles.gateWrap}>
          <Icon name="storefront" size={48} color={GOLD.muted} />
          <Text style={styles.gateTitle}>Merchants only</Text>
          <Text style={styles.gateBody}>
            Buy ads can only be posted by verified merchants. Apply for merchant
            status to unlock this feature.
          </Text>
          <TouchableOpacity
            style={styles.gateBtn}
            onPress={() => router.push('/merchants/merchant-request')}
            activeOpacity={0.85}
          >
            <Text style={styles.gateBtnText}>Become a merchant</Text>
          </TouchableOpacity>
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <Text style={styles.heading}>Post a Buy Ad</Text>
          <Text style={styles.subheading}>
            Tell users how much crypto you want to buy and at what rate. You pay
            fiat; the user&apos;s crypto is deposited into escrow at trade open.
          </Text>

          {/* Token selector */}
          <Text style={styles.label}>Token to buy</Text>
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

          {/* Fiat currency */}
          <Text style={styles.label}>Fiat currency</Text>
          <CurrencySelector
            variant="card"
            selectedCode={currencyCode}
            onSelectCode={setAdCurrencyCode}
          />

          {/* Total amount to buy */}
          <Text style={styles.label}>Total {token.symbol} to buy</Text>
          <View style={styles.inputCard}>
            <Text style={styles.inputPrefix}>{token.symbol}</Text>
            <TextInput
              style={styles.inputField}
              placeholder="e.g. 100"
              placeholderTextColor="#999"
              keyboardType="decimal-pad"
              value={amountInput}
              onChangeText={setAmountInput}
            />
          </View>

          {/* Rate */}
          <Text style={styles.label}>Rate ({currencyCode} per {token.symbol})</Text>
          <View style={styles.inputCard}>
            <Text style={styles.inputPrefix}>{symbol}</Text>
            <TextInput
              style={styles.inputField}
              placeholder="e.g. 1600"
              placeholderTextColor="#999"
              keyboardType="decimal-pad"
              value={rateInput}
              onChangeText={setRateInput}
            />
          </View>

          {/* Limits */}
          <Text style={styles.label}>Limits (optional)</Text>
          <View style={styles.limitsRow}>
            <View style={[styles.inputCard, styles.limitInput]}>
              <Text style={styles.inputPrefix}>Min {symbol}</Text>
              <TextInput
                style={styles.inputField}
                placeholder="0"
                placeholderTextColor="#999"
                keyboardType="decimal-pad"
                value={minLimitInput}
                onChangeText={setMinLimitInput}
              />
            </View>
            <View style={[styles.inputCard, styles.limitInput]}>
              <Text style={styles.inputPrefix}>Max {symbol}</Text>
              <TextInput
                style={styles.inputField}
                placeholder="0"
                placeholderTextColor="#999"
                keyboardType="decimal-pad"
                value={maxLimitInput}
                onChangeText={setMaxLimitInput}
              />
            </View>
          </View>

          {/* Payment methods — where you'll send fiat FROM */}
          <Text style={styles.label}>Your payment methods (where you&apos;ll pay from)</Text>
          {loadingPms ? (
            <ActivityIndicator color={GOLD.dark} style={{ marginVertical: 12 }} />
          ) : paymentMethods.length === 0 ? (
            <Text style={styles.placeholder}>
              No {currencyCode} payment methods found.{' '}
              <Text
                style={styles.link}
                onPress={() => router.push('/p2p/payment-methods')}
              >
                Add one
              </Text>
              {' '}first.
            </Text>
          ) : (
            paymentMethods.map((m) => (
              <TouchableOpacity
                key={m.id}
                style={[styles.pmCard, selectedPmIds.includes(m.id) && styles.pmCardActive]}
                onPress={() => togglePm(m.id)}
                activeOpacity={0.8}
              >
                <Icon
                  name={
                    selectedPmIds.includes(m.id) ? 'check-box' : 'check-box-outline-blank'
                  }
                  size={22}
                  color={selectedPmIds.includes(m.id) ? GOLD.dark : '#999'}
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

          {/* Terms */}
          <Text style={styles.label}>Terms (optional)</Text>
          <TextInput
            style={styles.termsInput}
            placeholder="Any special instructions for sellers…"
            placeholderTextColor="#999"
            multiline
            numberOfLines={3}
            value={termsInput}
            onChangeText={setTermsInput}
            maxLength={500}
          />

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
              <Text style={styles.submitText}>Publish buy ad</Text>
            )}
          </TouchableOpacity>

          <Text style={styles.disclaimer}>
            Users who respond will deposit {token.symbol} into escrow. You&apos;ll be
            notified to send fiat and mark as paid.
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 80 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 6 },
  subheading: { fontSize: 14, color: '#666', lineHeight: 20, marginBottom: 20 },
  label: { fontSize: 14, fontWeight: '700', color: '#333', marginTop: 16, marginBottom: 6 },
  tokenRow: { flexDirection: 'row', gap: 10, marginBottom: 4 },
  tokenChip: {
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 8,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  tokenChipActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  tokenChipText: { fontWeight: '700', color: GOLD.dark, fontSize: 13 },
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
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0, fontWeight: '600' },
  inputPrefix: { fontSize: 14, fontWeight: '700', color: GOLD.dark, marginRight: 8 },
  limitsRow: { flexDirection: 'row', gap: 10 },
  limitInput: { flex: 1 },
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
  pmBank: { fontSize: 14, fontWeight: '600', color: '#000' },
  pmAccount: { fontSize: 12, color: '#666', marginTop: 2 },
  placeholder: { fontSize: 13, color: '#888', paddingVertical: 12, fontStyle: 'italic' },
  link: { color: GOLD.dark, fontWeight: '700', textDecorationLine: 'underline' },
  termsInput: {
    backgroundColor: '#FFF',
    borderRadius: 10,
    padding: 12,
    borderWidth: 1,
    borderColor: GOLD.muted,
    color: '#000',
    fontSize: 14,
    textAlignVertical: 'top',
    minHeight: 80,
  },
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
  gateWrap: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    paddingHorizontal: 40,
    gap: 12,
  },
  gateTitle: { fontSize: 20, fontWeight: '800', color: '#000' },
  gateBody: { fontSize: 14, color: '#666', textAlign: 'center', lineHeight: 20, marginBottom: 8 },
  gateBtn: {
    backgroundColor: GOLD.primary,
    paddingHorizontal: 18,
    paddingVertical: 10,
    borderRadius: 8,
  },
  gateBtnText: { color: '#000', fontWeight: '800' },
});

export default CreateBuyAdScreen;
