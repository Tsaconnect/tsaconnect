// app/wallet/mcgp/sell.tsx
//
// Three "reduce a balance" actions in one screen:
//   • Sell premium  — premium MCGP -> USDC at the premium sell price.
//   • Redeem legacy — pre-launch legacy MCGP -> USDC at the legacy price
//                     (redeem-only; capped at the snapshot entitlement).
//   • Withdraw      — premium MCGP -> your wallet (becomes non-spendable).
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, Icon, SegmentedControl, Spinner,
  SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { getMcgpQuote } from '@/services/mcgpSwapApi';
import { executeSellPremium, executeLegacyRedeem, executeWithdraw } from '@/services/mcgpFlow';

type Mode = 'premium' | 'legacy' | 'withdraw';
type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const fromUsdc = (b: string) => Number(ethers.formatUnits(b || '0', 6)).toFixed(2);

const MODES: { key: Mode; label: string }[] = [
  { key: 'premium', label: 'Sell' },
  { key: 'legacy', label: 'Redeem' },
  { key: 'withdraw', label: 'Withdraw' },
];

export default function SellMcgp() {
  const { colors, spacing } = useTheme();
  const [mode, setMode] = useState<Mode>('premium');
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
      if (mode === 'premium' || mode === 'legacy') {
        const q = await getMcgpQuote(mode === 'premium' ? 'premiumSell' : 'legacySell', base);
        if (!q.success || !q.data) throw new Error(q.message || 'Quote failed');
        setUsdcOut(q.data.usdcAmount);
      } else {
        setUsdcOut('0');
      }
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
      if (mode === 'premium') await executeSellPremium(base, usdcOut);
      else if (mode === 'legacy') await executeLegacyRedeem(base, usdcOut);
      else await executeWithdraw(base);
      setScreen('success');
    } catch (e: any) {
      setError(e.message || 'Transaction failed');
      setScreen('failure');
    }
  };

  const verb = mode === 'withdraw' ? 'Withdraw' : mode === 'legacy' ? 'Redeem' : 'Sell';

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
          {ok ? 'Done' : `${verb} failed`}
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
              <SummaryRow label={mode === 'withdraw' ? 'Withdraw' : 'You give'} value={`${amount} MCGP`} />
              {mode !== 'withdraw' ? (
                <>
                  <Divider spacing="xs" />
                  <SummaryRow label="You receive" value={`$${fromUsdc(usdcOut)} USDC`} />
                </>
              ) : (
                <SummaryRow label="To" value="Your wallet (non-spendable)" />
              )}
            </SummaryCard>
          </Card>
          <Button title={verb} onPress={() => setShowAuth(true)} />
          <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
        </ScrollView>
        <AuthorizationModal
          visible={showAuth}
          title={`Authorize ${verb.toLowerCase()}`}
          description={
            mode === 'withdraw'
              ? `Withdraw ${amount} MCGP to your wallet`
              : `${verb} ${amount} MCGP for $${fromUsdc(usdcOut)} USDC`
          }
          onAuthorized={handleGo}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <SegmentedControl segments={MODES} value={mode} onChange={(m) => { setMode(m); setError(''); }} />
        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          {mode === 'premium'
            ? 'Sell spendable premium MCGP back for USDC.'
            : mode === 'legacy'
              ? 'Redeem your pre-launch legacy MCGP for USDC (capped at your entitlement).'
              : 'Move premium MCGP to your wallet. Withdrawn MCGP is no longer spendable in the app.'}
        </Text>
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
