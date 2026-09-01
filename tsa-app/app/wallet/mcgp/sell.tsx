// app/wallet/mcgp/sell.tsx
//
// Sell spendable premium MCGP back for USDC at the premium sell price.
// (Withdraw-to-wallet is not offered.)
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, Icon, PressableScale, Spinner,
  SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { getMcgpQuote } from '@/services/mcgpSwapApi';
import { executeSellPremium } from '@/services/mcgpFlow';
import { useMcgpBalances } from '@/hooks/useMcgpBalances';
import { NoLocalWalletScreen } from '@/components/wallet/NoLocalWallet';

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const fromUsdc = (b: string) => Number(ethers.formatUnits(b || '0', 6)).toFixed(2);

export default function SellMcgp() {
  const { colors, spacing } = useTheme();
  const { usdc, mcgpPremium, mcgpPremiumStr, loading: balLoading, walletMissing } = useMcgpBalances();
  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [usdcOut, setUsdcOut] = useState('0');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showAuth, setShowAuth] = useState(false);

  const base = (() => {
    try {
      return ethers.parseUnits(amount || '0', 18).toString();
    } catch {
      return '0';
    }
  })();

  const handleReview = async () => {
    setError('');
    if (!amount || Number(amount) <= 0) {
      setError('Enter an amount');
      return;
    }
    try {
      setLoading(true);
      const q = await getMcgpQuote('premiumSell', base);
      if (!q.success || !q.data) throw new Error(q.message || 'Quote failed');
      setUsdcOut(q.data.usdcAmount);
      setScreen('confirm');
    } catch (e: any) {
      setError(e.message || 'Could not get a quote');
    } finally {
      setLoading(false);
    }
  };

  const handleGo = async () => {
    setShowAuth(false);
    setScreen('sending');
    setError('');
    try {
      await executeSellPremium(base, usdcOut);
      setScreen('success');
    } catch (e: any) {
      setError(e.message || 'Transaction failed');
      setScreen('failure');
    }
  };

  if (screen === 'sending') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>
          Processing…
        </Text>
      </SafeAreaView>
    );
  }

  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name={ok ? 'checkCircle' : 'closeCircle'} size={64} color={ok ? colors.success : colors.danger} />
        <Text variant="title1" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>
          {ok ? 'Done' : 'Sell failed'}
        </Text>
        {!ok ? (
          <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>
            {error}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        <Button title="Done" onPress={() => router.replace('/wallet/mcgp')} />
        {!ok ? (
          <View style={{ marginTop: spacing.md }}>
            <Button title="Try again" variant="secondary" onPress={() => setScreen('form')} />
          </View>
        ) : null}
      </SafeAreaView>
    );
  }

  if (screen === 'confirm') {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <SummaryCard>
              <SummaryRow label="You give" value={`${amount} MCGP`} />
              <Divider spacing="xs" />
              <SummaryRow label="You receive" value={`$${fromUsdc(usdcOut)} USDC`} />
            </SummaryCard>
          </Card>
          <Button title="Sell" onPress={() => setShowAuth(true)} />
          <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
        </ScrollView>
        <AuthorizationModal
          visible={showAuth}
          title="Authorize sell"
          description={`Sell ${amount} MCGP for $${fromUsdc(usdcOut)} USDC`}
          onAuthorized={handleGo}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // No signing key here — never render a balance the user cannot spend.
  if (!balLoading && walletMissing) return <NoLocalWalletScreen action="sell MCGP" />;

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          Sell spendable premium MCGP back for USDC.
        </Text>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="bodySm" color="textSecondary">
            Balance: {mcgpPremium.toLocaleString(undefined, { maximumFractionDigits: 2 })} MCGP · ${usdc.toFixed(2)} USDC
          </Text>
          <PressableScale onPress={() => { setAmount(mcgpPremiumStr); setError(''); }} accessibilityLabel="Use max">
            <Text variant="bodySm" weight="700" color="accent">Max</Text>
          </PressableScale>
        </View>
        <AmountInput
          label="Amount"
          value={amount}
          onChangeText={setAmount}
          symbol="MCGP"
          symbolPosition="suffix"
          error={error || undefined}
        />
        <Button title="Review" onPress={handleReview} loading={loading} />
      </ScrollView>
    </SafeAreaView>
  );
}
