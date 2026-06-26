// app/wallet/mcgp/buy.tsx
//
// Buy spendable PREMIUM MCGP with USDC. Quotes at the premium price, approves
// USDC if needed, signs buyPremium, then confirms. form→confirm→sending→result.
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, Icon, Spinner, SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { getMcgpQuote } from '@/services/mcgpSwapApi';
import { executeBuy } from '@/services/mcgpFlow';

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const toBase = (v: string, d: number) => ethers.parseUnits(v || '0', d).toString();
const fromUsdc = (b: string) => Number(ethers.formatUnits(b || '0', 6)).toFixed(2);

export default function BuyMcgp() {
  const { colors, spacing } = useTheme();
  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [usdcCost, setUsdcCost] = useState('0'); // base units (6dp)
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showAuth, setShowAuth] = useState(false);

  const mcgpBase = (() => {
    try {
      return toBase(amount, 18);
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
      const q = await getMcgpQuote('premiumBuy', mcgpBase);
      if (!q.success || !q.data) throw new Error(q.message || 'Quote failed');
      setUsdcCost(q.data.usdcAmount);
      setScreen('confirm');
    } catch (e: any) {
      setError(e.message || 'Could not get a quote');
    } finally {
      setLoading(false);
    }
  };

  const handleBuy = async () => {
    setShowAuth(false);
    setScreen('sending');
    setError('');
    try {
      await executeBuy(mcgpBase, usdcCost);
      setScreen('success');
    } catch (e: any) {
      setError(e.message || 'Purchase failed');
      setScreen('failure');
    }
  };

  if (screen === 'sending') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>
          Approving &amp; buying…
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
          {ok ? 'Purchase complete' : 'Purchase failed'}
        </Text>
        {ok ? (
          <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
            {amount} MCGP added to your spendable balance.
          </Text>
        ) : (
          <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>
            {error}
          </Text>
        )}
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
              <SummaryRow label="You buy" value={`${amount} MCGP`} />
              <Divider spacing="xs" />
              <SummaryRow label="You pay" value={`$${fromUsdc(usdcCost)} USDC`} />
              <SummaryRow label="Type" value="Premium (spendable)" />
            </SummaryCard>
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              If USDC isn&apos;t yet approved for the swap, you&apos;ll sign an approval first.
            </Text>
          </Card>
          <Button title={`Buy for $${fromUsdc(usdcCost)}`} onPress={() => setShowAuth(true)} />
          <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
        </ScrollView>
        <AuthorizationModal
          visible={showAuth}
          title="Authorize purchase"
          description={`Buy ${amount} MCGP for $${fromUsdc(usdcCost)} USDC`}
          onAuthorized={handleBuy}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <AmountInput
          label="Amount to buy"
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
