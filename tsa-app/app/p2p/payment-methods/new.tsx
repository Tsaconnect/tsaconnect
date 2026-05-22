// app/p2p/payment-methods/new.tsx
//
// Form to add a bank or mobile-money account. Phase 1 has no Flutterwave
// account-name resolution — the user types the account name themselves.
// Server side persists `verified: false` and a later Phase-2 onboarding
// flow will flip it after FW verifies the account.
import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  FlatList,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
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
import {
  getNigerianBanks,
  NgBank,
  resolveAccountName,
} from '../../../services/ngBanks';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
};

const NewPaymentMethodScreen: React.FC = () => {
  const [type, setType] = useState<'bank' | 'mobile_money'>('bank');
  const [bank, setBank] = useState<NgBank | null>(null);
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(true);
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  const [isDefault, setIsDefault] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  // Name-enquiry state. `resolved` flags an auto-fill from Nomba so we can
  // lock the input + show a check; `resolveError` lets the user know to
  // either fix the digits or fall back to manual entry on outage.
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState(false);
  const [resolveError, setResolveError] = useState<
    null | 'not_found' | 'unavailable'
  >(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const list = await getNigerianBanks();
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Debounced account-name resolution. Fires when the user has both a
  // bank selected and a 10-digit account number. Re-firing on either
  // change cancels the in-flight call via the captured `cancelled` flag.
  // For mobile-money entries we skip the lookup — those aren't bank
  // accounts and Nomba won't resolve them.
  useEffect(() => {
    if (type !== 'bank' || !bank || accountNumber.length !== 10) {
      setResolving(false);
      setResolved(false);
      setResolveError(null);
      return;
    }
    let cancelled = false;
    setResolving(true);
    setResolved(false);
    setResolveError(null);
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(bank.code, accountNumber);
      if (cancelled) return;
      setResolving(false);
      if (res.ok) {
        setAccountName(res.accountName);
        setResolved(true);
      } else {
        setAccountName('');
        setResolveError(res.reason);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [type, bank, accountNumber]);

  const valid =
    !!bank &&
    accountNumber.trim().length >= 10 &&
    accountName.trim().length >= 2;

  const handleSubmit = async () => {
    if (!valid || submitting || !bank) return;
    setSubmitting(true);
    const res = await paymentMethodsApi.create({
      type,
      country: 'NG',
      currency: 'NGN',
      bankName: bank.name,
      bankCode: bank.code,
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
            onPress={() => setShowBankPicker(true)}
            activeOpacity={0.7}
          >
            <Text style={[styles.inputText, !bank && styles.placeholder]}>
              {bank?.name || 'Choose a bank'}
            </Text>
            {banksLoading ? (
              <ActivityIndicator size="small" color={GOLD.dark} />
            ) : (
              <Icon name="arrow-drop-down" size={24} color={GOLD.dark} />
            )}
          </TouchableOpacity>

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
          <View
            style={[
              styles.input,
              resolved && styles.inputVerified,
              resolveError === 'not_found' && styles.inputError,
            ]}
          >
            <TextInput
              style={styles.inputField}
              autoCapitalize="words"
              placeholder={
                type === 'bank'
                  ? 'Auto-fills when you enter the account number'
                  : 'As registered with the provider'
              }
              placeholderTextColor="#999"
              value={accountName}
              onChangeText={(v) => {
                setAccountName(v);
                if (resolved) setResolved(false);
              }}
              editable={!resolving && !resolved}
            />
            {resolving && <ActivityIndicator size="small" color={GOLD.dark} />}
            {resolved && (
              <TouchableOpacity
                onPress={() => {
                  setResolved(false);
                  setAccountName('');
                }}
                hitSlop={10}
                style={styles.inputAction}
              >
                <Icon name="check-circle" size={20} color="#2E8B57" />
              </TouchableOpacity>
            )}
          </View>
          {resolveError === 'not_found' && (
            <Text style={styles.fieldError}>
              We couldn’t find this account at the selected bank. Check the
              digits or bank.
            </Text>
          )}
          {resolveError === 'unavailable' && (
            <Text style={styles.fieldHint}>
              Account verification is offline. You can type the name manually.
            </Text>
          )}

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

      <BankPickerModal
        visible={showBankPicker}
        banks={banks}
        loading={banksLoading}
        selectedCode={bank?.code}
        onSelect={(b) => {
          setBank(b);
          setShowBankPicker(false);
        }}
        onClose={() => setShowBankPicker(false)}
      />
    </SafeAreaView>
  );
};

interface BankPickerModalProps {
  visible: boolean;
  banks: NgBank[];
  loading: boolean;
  selectedCode?: string;
  onSelect: (bank: NgBank) => void;
  onClose: () => void;
}

const BankPickerModal: React.FC<BankPickerModalProps> = ({
  visible,
  banks,
  loading,
  selectedCode,
  onSelect,
  onClose,
}) => {
  const [query, setQuery] = useState('');

  useEffect(() => {
    if (!visible) setQuery('');
  }, [visible]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return banks;
    return banks.filter((b) => b.name.toLowerCase().includes(q));
  }, [banks, query]);

  return (
    <Modal
      visible={visible}
      animationType="slide"
      transparent
      onRequestClose={onClose}
    >
      <Pressable style={styles.modalBackdrop} onPress={onClose}>
        <Pressable style={styles.modalSheet} onPress={() => {}}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Choose a bank</Text>
            <TouchableOpacity onPress={onClose} hitSlop={10}>
              <Icon name="close" size={24} color="#333" />
            </TouchableOpacity>
          </View>

          <View style={styles.searchRow}>
            <Icon name="search" size={20} color="#888" />
            <TextInput
              style={styles.searchInput}
              placeholder="Search bank"
              placeholderTextColor="#999"
              value={query}
              onChangeText={setQuery}
              autoCorrect={false}
              autoCapitalize="none"
            />
            {query.length > 0 && (
              <TouchableOpacity onPress={() => setQuery('')} hitSlop={8}>
                <Icon name="cancel" size={18} color="#aaa" />
              </TouchableOpacity>
            )}
          </View>

          {loading && banks.length === 0 ? (
            <View style={styles.modalEmpty}>
              <ActivityIndicator color={GOLD.dark} />
              <Text style={styles.modalEmptyText}>Loading banks…</Text>
            </View>
          ) : filtered.length === 0 ? (
            <View style={styles.modalEmpty}>
              <Text style={styles.modalEmptyText}>No banks match “{query}”.</Text>
            </View>
          ) : (
            <FlatList
              data={filtered}
              keyExtractor={(b) => b.code}
              keyboardShouldPersistTaps="handled"
              renderItem={({ item }) => {
                const selected = item.code === selectedCode;
                return (
                  <TouchableOpacity
                    style={[styles.pickerRow, selected && styles.pickerRowSelected]}
                    onPress={() => onSelect(item)}
                    activeOpacity={0.7}
                  >
                    <Text style={styles.pickerText}>{item.name}</Text>
                    {selected && (
                      <Icon name="check" size={20} color={GOLD.dark} />
                    )}
                  </TouchableOpacity>
                );
              }}
            />
          )}
        </Pressable>
      </Pressable>
    </Modal>
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
  inputVerified: { borderColor: '#2E8B57', backgroundColor: '#F1FAF4' },
  inputError: { borderColor: GOLD.error },
  inputAction: { paddingLeft: 8 },
  fieldError: { color: GOLD.error, fontSize: 12, marginTop: 6 },
  fieldHint: { color: '#666', fontSize: 12, marginTop: 6 },
  pickerRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 18,
    paddingVertical: 14,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  pickerRowSelected: { backgroundColor: GOLD.light },
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
  modalBackdrop: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.45)',
    justifyContent: 'flex-end',
  },
  modalSheet: {
    backgroundColor: '#FFF',
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: '85%',
    paddingBottom: Platform.OS === 'ios' ? 24 : 12,
  },
  modalHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingTop: 18,
    paddingBottom: 10,
  },
  modalTitle: { fontSize: 17, fontWeight: '700', color: '#000' },
  searchRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginHorizontal: 16,
    marginBottom: 8,
    paddingHorizontal: 12,
    paddingVertical: 8,
    backgroundColor: '#F5F5F5',
    borderRadius: 10,
  },
  searchInput: {
    flex: 1,
    marginLeft: 8,
    fontSize: 15,
    color: '#000',
    paddingVertical: 0,
  },
  modalEmpty: {
    paddingVertical: 40,
    alignItems: 'center',
  },
  modalEmptyText: { marginTop: 8, color: '#777', fontSize: 14 },
});

export default NewPaymentMethodScreen;
