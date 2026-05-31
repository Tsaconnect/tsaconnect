// app/p2p/payment-methods/index.tsx
//
// List of saved bank/mobile-money accounts the user receives fiat through
// for P2P trades. Soft-deletes are blocked server-side when the method is
// referenced by an open trade; the UI surfaces the 409 message cleanly.
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui);
// data fetching, the delete confirm + 409 handling, and navigation are
// unchanged — this is a visual/UX restyle only.
import { router } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paymentMethodsApi, type P2PPaymentMethod } from '../../../services/p2p';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Divider,
  EmptyState,
  Icon,
  PressableScale,
  SkeletonGroup,
  Text,
} from '@/components/ui';

const PaymentMethodsScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
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
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ padding: spacing.lg }}>
          <SkeletonGroup shape="adCard" count={3} />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            tintColor={colors.accent}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text variant="title1" weight="800">
          Payment methods
        </Text>
        <Text
          variant="bodySm"
          color="textSecondary"
          style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}
        >
          The accounts buyers send fiat to during P2P trades.
        </Text>

        {methods.length === 0 ? (
          <EmptyState
            icon="wallet"
            title="No payment methods yet"
            subtitle="Add one before posting an ad so buyers know where to send fiat."
            actionLabel="Add payment method"
            onAction={() => router.push('/p2p/payment-methods/new')}
          />
        ) : (
          <>
            <View style={{ gap: spacing.md }}>
              {methods.map((m) => (
                <MethodCard key={m.id} method={m} onDelete={handleDelete} />
              ))}
            </View>

            <Button
              title="Add payment method"
              leftIcon="plus"
              onPress={() => router.push('/p2p/payment-methods/new')}
              style={{ marginTop: spacing.lg }}
            />
          </>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const MethodCard: React.FC<{
  method: P2PPaymentMethod;
  onDelete: (m: P2PPaymentMethod) => void;
}> = ({ method: m, onDelete }) => {
  const { colors, spacing } = useTheme();

  return (
    <AccentCard>
      <View style={{ flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between', gap: spacing.sm }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}>
          <Icon name={m.type === 'mobile_money' ? 'mobile' : 'bank'} size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flex: 1 }}>
            {m.bankName}
          </Text>
        </View>
        {m.isDefault && (
          <Badge label="Default" color={colors.accent} background={colors.accentMuted} size="sm" />
        )}
      </View>

      <Text variant="title3" weight="600" numeric style={{ marginTop: spacing.sm, letterSpacing: 1 }}>
        {maskAccount(m.accountNumber)}
      </Text>
      <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
        {m.accountName}
      </Text>

      <Divider spacing="md" />

      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">
            {m.currency} · {m.country}
          </Text>
          {m.verified && (
            <Badge label="Verified" icon="checkCircle" color={colors.success} background={colors.successBg} size="sm" />
          )}
        </View>
        <PressableScale
          onPress={() => onDelete(m)}
          style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, paddingVertical: spacing.xs }}
        >
          <Icon name="trash" size={16} color={colors.danger} />
          <Text variant="bodySm" weight="700" color="danger">
            Remove
          </Text>
        </PressableScale>
      </View>
    </AccentCard>
  );
};

// Mask all but the last 4 digits — keeps screenshots/screen-shares safer.
function maskAccount(account: string): string {
  if (account.length <= 4) return account;
  return '•'.repeat(account.length - 4) + account.slice(-4);
}

export default PaymentMethodsScreen;
