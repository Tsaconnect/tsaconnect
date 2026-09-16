// app/wallet/mcgp/index.tsx
//
// MCGP hub: shows the spendable PREMIUM balance and routes to buy / sell / pay.
import React, { useCallback, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { AccentCard, Button, Spinner, Text } from '@/components/ui';
import { getMcgpBalance, type McgpBalance } from '@/services/mcgpSwapApi';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { NoLocalWalletScreen } from '@/components/wallet/NoLocalWallet';

const fmtMcgp = (base?: string) => {
  try {
    return Number(ethers.formatUnits(base || '0', 18)).toLocaleString(undefined, {
      maximumFractionDigits: 4,
    });
  } catch {
    return '0';
  }
};

export default function McgpHub() {
  const { colors, spacing } = useTheme();
  const [bal, setBal] = useState<McgpBalance | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const { address: signer, ready: signerReady } = useSignableWallet();

  const load = useCallback(async () => {
    setError('');
    // useSignableWallet already resolved this; re-reading SecureStore here
    // would be a second keychain round trip for an answer we hold.
    if (!signerReady) return;
    if (!signer) { setLoading(false); return; }
    const r = await getMcgpBalance();
    if (r.success && r.data) setBal(r.data);
    else setError(r.message || 'Failed to load balance');
    setLoading(false);
  }, [signer, signerReady]);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  // The hub's whole content is a balance and the actions that spend it, so
  // without a local key there is nothing honest to show.
  if (signerReady && !signer) return <NoLocalWalletScreen action="spend MCGP" />;

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        <AccentCard>
          <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
            Spendable MCGP (Premium)
          </Text>
          <Text variant="title1" weight="700" numeric style={{ color: colors.textPrimary, marginTop: spacing.xs }}>
            {loading ? '—' : fmtMcgp(bal?.premiumBalance)} MCGP
          </Text>
          <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
            Spend it in the marketplace and pay other users.
          </Text>
          <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.sm }}>
            Sellable for USDC: {loading ? '—' : fmtMcgp(bal?.premiumBought)} MCGP
          </Text>
          <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
            Bought with naira (in-app only): {loading ? '—' : fmtMcgp(bal?.premiumCredited)} MCGP
          </Text>
        </AccentCard>

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <View style={{ flex: 1 }}>
            <Button title="Buy" onPress={() => router.push('/wallet/mcgp/buy')} />
          </View>
          <View style={{ flex: 1 }}>
            <Button title="Pay" variant="secondary" onPress={() => router.push('/wallet/mcgp/pay')} />
          </View>
          <View style={{ flex: 1 }}>
            <Button title="Sell" variant="secondary" onPress={() => router.push('/wallet/mcgp/sell')} />
          </View>
        </View>

        {loading ? <Spinner /> : null}
      </ScrollView>
    </SafeAreaView>
  );
}
