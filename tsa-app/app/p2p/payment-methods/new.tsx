// app/p2p/payment-methods/new.tsx
//
// Form to add a bank or mobile-money account. Phase 1 has no Flutterwave
// account-name resolution — the user types the account name themselves.
// Server side persists `verified: false` and a later Phase-2 onboarding
// flow will flip it after FW verifies the account.
import { router } from 'expo-router';
import React, { useState } from 'react';
import {
  Alert,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paymentMethodsApi } from '../../../services/p2p';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
};

// Curated initial list — matches the FW-supported fintech-first banks
// PM called out (OPay, PalmPay, Moniepoint, Kuda, GTBank). Phase 2 will
// auto-populate from FW's banks endpoint.
const NG_BANKS = [
  'OPay',
  'PalmPay',
  'Moniepoint',
  'Kuda Bank',
  'GTBank',
  'Access Bank',
  'First Bank',
  'UBA',
  'Zenith Bank',
  'Fidelity Bank',
  'Union Bank',
  'Ecobank',
  'Stanbic IBTC',
  'Sterling Bank',
  'Wema Bank',
  'FCMB',
  'Polaris Bank',
];

const NewPaymentMethodScreen: React.FC = () => {
  const [type, setType] = useState<'bank' | 'mobile_money'>('bank');
  const [bankName, setBankName] = useState('');
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  const [isDefault, setIsDefault] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const valid =
    bankName.trim().length > 0 &&
    accountNumber.trim().length >= 10 &&
    accountName.trim().length >= 2;

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
    const res = await paymentMethodsApi.create({
      type,
      country: 'NG',
      currency: 'NGN',
      bankName: bankName.trim(),
      accountNumber: accountNumber.trim(),
      accountName: accountName.trim(),
      isDefault,
    });
    setSubmitting(false);
    if (res.success) {
      router.back();
    } else {
      Alert.alert('Could not save', res.message || 'Server rejected the request');
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <View style={styles.typeRow}>
            {(['bank', 'mobile_money'] as const).map((t) => (
              <TouchableOpacity
                key={t}
                style={[styles.typeChip, type === t && styles.typeChipActive]}
                onPress={() => setType(t)}
                activeOpacity={0.8}
              >
                <Icon
                  name={t === 'bank' ? 'account-balance' : 'phone-iphone'}
                  size={18}
                  color={type === t ? '#000' : GOLD.dark}
                />
                <Text
                  style={[styles.typeChipText, type === t && styles.typeChipTextActive]}
                >
                  {t === 'bank' ? 'Bank' : 'Mobile money'}
                </Text>
              </TouchableOpacity>
            ))}
          </View>

          <Text style={styles.label}>Bank</Text>
          <TouchableOpacity
            style={styles.input}
            onPress={() => setShowBankPicker((s) => !s)}
            activeOpacity={0.7}
          >
            <Text style={[styles.inputText, !bankName && styles.placeholder]}>
              {bankName || 'Choose a bank'}
            </Text>
            <Icon name="arrow-drop-down" size={24} color={GOLD.dark} />
          </TouchableOpacity>
          {showBankPicker && (
            <View style={styles.pickerCard}>
              {NG_BANKS.map((b) => (
                <TouchableOpacity
                  key={b}
                  style={styles.pickerRow}
                  onPress={() => {
                    setBankName(b);
                    setShowBankPicker(false);
                  }}
                  activeOpacity={0.7}
                >
                  <Text style={styles.pickerText}>{b}</Text>
                </TouchableOpacity>
              ))}
            </View>
          )}

          <Text style={styles.label}>Account number</Text>
          <View style={styles.input}>
            <TextInput
              style={styles.inputField}
              keyboardType="number-pad"
              maxLength={20}
              placeholder="0123456789"
              placeholderTextColor="#999"
              value={accountNumber}
              onChangeText={(v) => setAccountNumber(v.replace(/\D/g, ''))}
            />
          </View>

          <Text style={styles.label}>Account name</Text>
          <View style={styles.input}>
            <TextInput
              style={styles.inputField}
              autoCapitalize="words"
              placeholder="As registered with the bank"
              placeholderTextColor="#999"
              value={accountName}
              onChangeText={setAccountName}
            />
          </View>

          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.switchLabel}>Set as default</Text>
              <Text style={styles.switchHint}>
                Pre-selected when you open a new trade.
              </Text>
            </View>
            <Switch
              value={isDefault}
              onValueChange={setIsDefault}
              trackColor={{ false: '#DDD', true: GOLD.muted }}
              thumbColor={isDefault ? GOLD.primary : '#FFF'}
            />
          </View>

          <View style={styles.note}>
            <Icon name="info" size={16} color={GOLD.dark} />
            <Text style={styles.noteText}>
              Buyers will see this account when they open a trade. Double-check the
              account number before saving.
            </Text>
          </View>

          <TouchableOpacity
            style={[styles.submit, (!valid || submitting) && styles.submitDisabled]}
            onPress={handleSubmit}
            disabled={!valid || submitting}
            activeOpacity={0.85}
          >
            <Text style={styles.submitText}>
              {submitting ? 'Saving…' : 'Save payment method'}
            </Text>
          </TouchableOpacity>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  typeRow: { flexDirection: 'row', gap: 10, marginBottom: 20 },
  typeChip: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 12,
    borderRadius: 10,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  typeChipActive: {
    backgroundColor: GOLD.primary,
    borderColor: GOLD.dark,
  },
  typeChipText: { marginLeft: 6, fontWeight: '600', color: GOLD.dark },
  typeChipTextActive: { color: '#000' },
  label: {
    fontSize: 14,
    fontWeight: '700',
    color: '#333',
    marginTop: 14,
    marginBottom: 6,
  },
  input: {
    backgroundColor: '#FFF',
    flexDirection: 'row',
    alignItems: 'center',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 12,
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  inputText: { flex: 1, fontSize: 16, color: '#000' },
  placeholder: { color: '#999' },
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0 },
  pickerCard: {
    backgroundColor: '#FFF',
    borderRadius: 10,
    marginTop: 4,
    maxHeight: 300,
    borderWidth: 1,
    borderColor: GOLD.muted,
    overflow: 'hidden',
  },
  pickerRow: {
    paddingHorizontal: 14,
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  pickerText: { fontSize: 15, color: '#000' },
  switchRow: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    padding: 14,
    borderRadius: 10,
    marginTop: 18,
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  switchLabel: { fontSize: 15, fontWeight: '600', color: '#000' },
  switchHint: { fontSize: 12, color: '#666', marginTop: 2 },
  note: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: GOLD.light,
    padding: 12,
    borderRadius: 10,
    marginTop: 16,
  },
  noteText: { flex: 1, marginLeft: 8, fontSize: 13, color: GOLD.dark, lineHeight: 18 },
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

export default NewPaymentMethodScreen;
