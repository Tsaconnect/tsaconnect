// app/p2p/payment-methods/index.tsx
//
// List of saved bank/mobile-money accounts the user receives fiat through
// for P2P trades. Soft-deletes are blocked server-side when the method is
// referenced by an open trade; the UI surfaces the 409 message cleanly.
import { router } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paymentMethodsApi, type P2PPaymentMethod } from '../../../services/p2p';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
};

const PaymentMethodsScreen: React.FC = () => {
  const [methods, setMethods] = useState<P2PPaymentMethod[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await paymentMethodsApi.list();
    if (res.success && res.data) {
      setMethods(res.data);
    } else if (res.message) {
      Alert.alert('Could not load payment methods', res.message);
    }
    setLoading(false);
    setRefreshing(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const handleDelete = (method: P2PPaymentMethod) => {
    Alert.alert(
      'Remove payment method?',
      `Remove ${method.bankName} (${maskAccount(method.accountNumber)})? If this method is bound to an open trade, the server will reject the deletion.`,
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Remove',
          style: 'destructive',
          onPress: async () => {
            const res = await paymentMethodsApi.remove(method.id);
            if (res.success) {
              setMethods((prev) => prev.filter((m) => m.id !== method.id));
            } else {
              Alert.alert('Cannot remove', res.message || 'Server rejected the request');
            }
          },
        },
      ]
    );
  };

  if (loading) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        contentContainerStyle={styles.scroll}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text style={styles.heading}>Payment methods</Text>
        <Text style={styles.subheading}>
          The accounts buyers send fiat to during P2P trades.
        </Text>

        {methods.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="account-balance-wallet" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              No payment methods yet — add one before posting an ad.
            </Text>
          </View>
        ) : (
          methods.map((m) => (
            <View key={m.id} style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon
                  name={m.type === 'mobile_money' ? 'phone-iphone' : 'account-balance'}
                  size={24}
                  color={GOLD.dark}
                />
                <Text style={styles.bankName}>{m.bankName}</Text>
                {m.isDefault && (
                  <View style={styles.defaultBadge}>
                    <Text style={styles.defaultBadgeText}>Default</Text>
                  </View>
                )}
              </View>
              <Text style={styles.account}>{maskAccount(m.accountNumber)}</Text>
              <Text style={styles.accountName}>{m.accountName}</Text>
              <View style={styles.metaRow}>
                <Text style={styles.meta}>
                  {m.currency} · {m.country}
                </Text>
                {m.verified && (
                  <Text style={[styles.meta, { color: '#28A745' }]}>✓ Verified</Text>
                )}
              </View>
              <TouchableOpacity
                style={styles.deleteButton}
                onPress={() => handleDelete(m)}
                activeOpacity={0.7}
              >
                <Icon name="delete-outline" size={20} color={GOLD.error} />
                <Text style={styles.deleteButtonText}>Remove</Text>
              </TouchableOpacity>
            </View>
          ))
        )}

        <TouchableOpacity
          style={styles.addButton}
          onPress={() => router.push('/p2p/payment-methods/new')}
          activeOpacity={0.85}
        >
          <Icon name="add-circle" size={24} color="#000" />
          <Text style={styles.addButtonText}>Add payment method</Text>
        </TouchableOpacity>
      </ScrollView>
    </SafeAreaView>
  );
};

// Mask all but the last 4 digits — keeps screenshots/screen-shares safer.
function maskAccount(account: string): string {
  if (account.length <= 4) return account;
  return '•'.repeat(account.length - 4) + account.slice(-4);
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 6 },
  subheading: { fontSize: 14, color: '#666', marginBottom: 20, lineHeight: 20 },
  empty: {
    alignItems: 'center',
    paddingVertical: 60,
  },
  emptyText: {
    marginTop: 12,
    fontSize: 14,
    color: '#888',
    textAlign: 'center',
    maxWidth: 240,
  },
  card: {
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 18,
    marginBottom: 14,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.06,
    shadowRadius: 4,
    elevation: 2,
  },
  cardHeader: { flexDirection: 'row', alignItems: 'center', marginBottom: 10 },
  bankName: { fontSize: 16, fontWeight: '700', color: '#000', marginLeft: 10, flex: 1 },
  defaultBadge: {
    backgroundColor: GOLD.light,
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: 8,
  },
  defaultBadgeText: { fontSize: 11, fontWeight: '700', color: GOLD.dark },
  account: { fontSize: 18, fontWeight: '600', color: '#000', letterSpacing: 1 },
  accountName: { fontSize: 14, color: '#444', marginTop: 4 },
  metaRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginTop: 10,
    marginBottom: 10,
  },
  meta: { fontSize: 12, color: '#888' },
  deleteButton: {
    flexDirection: 'row',
    alignItems: 'center',
    alignSelf: 'flex-start',
    paddingVertical: 6,
  },
  deleteButtonText: { color: GOLD.error, fontSize: 14, marginLeft: 4, fontWeight: '600' },
  addButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD.primary,
    paddingVertical: 16,
    borderRadius: 12,
    marginTop: 12,
    shadowColor: GOLD.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.25,
    shadowRadius: 6,
    elevation: 4,
  },
  addButtonText: { fontWeight: '800', color: '#000', marginLeft: 8, fontSize: 16 },
});

export default PaymentMethodsScreen;
