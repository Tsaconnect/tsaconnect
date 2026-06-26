// app/wallet/mcgp/pay.tsx
//
// Pay another TSA user from spendable PREMIUM MCGP by @username. Resolves the
// recipient, signs spend(), confirms. form→confirm→sending→result.
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, Icon, Input, Spinner, SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { resolveUsername, type ResolvedUser } from '@/services/walletApi';
import { executePay } from '@/services/mcgpFlow';

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

export default function PayMcgp() {
  const { colors, spacing } = useTheme();
  const [screen, setScreen] = useState<Screen>('form');
  const [username, setUsername] = useState('');
  const [amount, setAmount] = useState('');
  const [resolved, setResolved] = useState<ResolvedUser | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showAuth, setShowAuth] = useState(false);

  const handleReview = async () => {
    setError('');
    const u = username.trim().replace(/^@/, '');
    if (!u) {
      setError('Enter a username');
      return;
    }
    if (!amount || Number(amount) <= 0) {
      setError('Enter an amount');
      return;
    }
    try {
      setLoading(true);
      const r = await resolveUsername(u);
      if (!r.success || !r.data) throw new Error(r.message || 'User not found');
      setResolved(r.data);
      setUsername(r.data.username);
      setScreen('confirm');
    } catch (e: any) {
      setError(e.message || 'Could not resolve user');
    } finally {
      setLoading(false);
    }
  };

  const handlePay = async () => {
    setShowAuth(false);
    setScreen('sending');
    setError('');
    try {
      await executePay(resolved!.username, ethers.parseUnits(amount, 18).toString());
      setScreen('success');
    } catch (e: any) {
      setError(e.message || 'Payment failed');
      setScreen('failure');
    }
  };

  if (screen === 'sending') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>
          Sending payment…
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
          {ok ? 'Payment sent' : 'Payment failed'}
        </Text>
        {ok ? (
          <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
            {amount} MCGP to @{resolved?.username}
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
              <SummaryRow label="To" value={`@${resolved?.username}`} />
              <SummaryRow label="Name" value={resolved?.name || '—'} />
              <Divider spacing="xs" />
              <SummaryRow label="Amount" value={`${amount} MCGP`} />
            </SummaryCard>
          </Card>
          <Button title="Send" onPress={() => setShowAuth(true)} />
          <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
        </ScrollView>
        <AuthorizationModal
          visible={showAuth}
          title="Authorize payment"
          description={`Send ${amount} MCGP to @${resolved?.username}`}
          onAuthorized={handlePay}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <Input
          label="Recipient username"
          autoCapitalize="none"
          placeholder="@username"
          value={username}
          onChangeText={setUsername}
        />
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
