// app/pay.tsx
//
// TSA Pay's payer-facing checkout — opened from a merchant's pay link
// (`/pay?id=<uuid>`). Test mode runs Approve/Decline entirely client-side
// through submitTestOutcome, gated by the same PIN/biometric authorization as
// every other money-moving action (see AuthorizationModal.tsx — no new
// unlock path here). Live mode is a placeholder until Parts B/C wire real
// crypto/fiat settlement onto this screen.
//
// State is computed by payScreenState (utils/payCheckout.ts) — this file
// only wires data in and renders whatever it says.
import React, { useCallback, useEffect, useState } from 'react';
import { Linking, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router, useLocalSearchParams } from 'expo-router';
import { useAuth } from '@/AuthContext/AuthContext';
import { useTheme } from '@/theme';
import { useDefaultScreenOptions } from '@/constants/navigation';
import {
  AccentCard,
  BottomActionBar,
  Button,
  Card,
  ErrorState,
  Icon,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { getCheckout, returnUrlFor, type PayCheckout, type PayStatus } from '@/services/payApi';
import { payScreenState } from '@/utils/payCheckout';
import { rememberPendingPayment } from '@/utils/postAuthRoute';
import { usePayTest } from '@/hooks/usePayTest';
import { formatKobo } from '@/utils/stableSellQuote';

const DONE_LABEL: Record<PayStatus, string> = {
  succeeded: 'Payment complete',
  failed: 'Payment failed',
  expired: 'Payment link expired',
  pending: 'Payment complete',
  processing: 'Payment complete',
};

export default function PayScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { id } = useLocalSearchParams<{ id?: string }>();
  const { isAuthenticated, isHydrated } = useAuth();
  const headerOptions = useDefaultScreenOptions({ backFallback: '/' });

  const [checkout, setCheckout] = useState<PayCheckout | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);
  const [now, setNow] = useState(() => Date.now());

  const state = payScreenState({ id, authed: isAuthenticated, checkout, error, now });

  const fetchCheckout = useCallback(async () => {
    if (typeof id !== 'string') return;
    try {
      const c = await getCheckout(id);
      setCheckout(c);
      setError(undefined);
    } catch (e: any) {
      setError(e?.message ?? 'Could not load this payment.');
    }
  }, [id]);

  // Initial load, once we actually know the customer is signed in.
  useEffect(() => {
    if (!isHydrated || !isAuthenticated || typeof id !== 'string') return;
    fetchCheckout();
  }, [isHydrated, isAuthenticated, id, fetchCheckout]);

  // Keep `now` fresh for the expiry check, and re-poll while our own attempt
  // is still settling (payScreenState's 'waiting' branch) — nothing else
  // moves this payment's status on its own.
  useEffect(() => {
    const t = setInterval(() => {
      setNow(Date.now());
      if (state.kind === 'waiting') fetchCheckout();
    }, 5000);
    return () => clearInterval(t);
  }, [state.kind, fetchCheckout]);

  const payTest = usePayTest(typeof id === 'string' ? id : '');

  useEffect(() => {
    if (payTest.result) setCheckout(payTest.result);
  }, [payTest.result]);

  useEffect(() => {
    if (!payTest.error) return;
    toast.error(payTest.error);
    // A failed test-outcome (someone else took the payment, or it expired)
    // means our checkout state is stale — re-fetch so the screen reflects
    // reality instead of keeping the Approve/Decline buttons up.
    fetchCheckout();
  }, [payTest.error, toast, fetchCheckout]);

  const handleSignIn = async () => {
    if (typeof id === 'string') await rememberPendingPayment(id);
    router.replace('/login');
  };

  const handleReturnToStore = async () => {
    if (!checkout) return;
    const url = returnUrlFor(checkout);
    if (!url) return;
    try {
      await Linking.openURL(url);
    } catch {
      toast.error('Could not open the store');
    }
  };

  if (!isHydrated) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <>
      <Stack.Screen
        options={{
          ...headerOptions,
          headerShown: true,
          title: checkout?.merchantName ? `Pay ${checkout.merchantName}` : 'Pay',
        }}
      />
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg, flexGrow: 1 }}>
          {state.kind === 'invalid' && <ErrorState title="This payment link isn't valid" />}

          {state.kind === 'loading' && (
            <View style={{ paddingVertical: spacing['4xl'], alignItems: 'center' }}>
              <Spinner size="large" />
            </View>
          )}

          {state.kind === 'error' && (
            <ErrorState title="Could not load this payment" subtitle={state.message} onRetry={fetchCheckout} />
          )}

          {state.kind === 'signin' && (
            <View style={{ alignItems: 'center', gap: spacing.lg, paddingVertical: spacing['3xl'] }}>
              <Icon name="storefront" size={40} color={colors.accent} />
              <Text variant="title3" weight="700" align="center">
                Sign in to pay
              </Text>
              <Button title="Sign in" size="lg" onPress={handleSignIn} />
            </View>
          )}

          {(state.kind === 'payable' || state.kind === 'waiting' || state.kind === 'blocked') && (
            <>
              {state.checkout.mode === 'test' && (
                <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg }}>
                  <Text variant="bodySm" weight="700" color="warning">
                    TEST PAYMENT — no money moves
                  </Text>
                </AccentCard>
              )}

              <Card>
                <SummaryCard>
                  <SummaryRow label="Paying" value={state.checkout.merchantName} />
                  <SummaryRow label="Amount" value={formatKobo(String(state.checkout.customerTotal))} total />
                  {state.checkout.fee > 0 && (
                    <SummaryRow label="Fee" value={`includes ${formatKobo(String(state.checkout.fee))} fee`} />
                  )}
                </SummaryCard>
              </Card>

              {state.kind === 'blocked' && (
                <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg }}>
                  <Text variant="bodySm" color="danger">
                    {state.why}
                  </Text>
                </AccentCard>
              )}

              {state.kind === 'waiting' && (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Spinner inline />
                  <Text variant="bodySm" color="textSecondary">
                    Waiting for confirmation…
                  </Text>
                </View>
              )}

              {state.kind === 'payable' && state.checkout.mode === 'live' && (
                <ErrorState title="Live payments aren't available in this version of the app yet." />
              )}
            </>
          )}

          {state.kind === 'done' && (
            <View style={{ alignItems: 'center', gap: spacing.lg, paddingVertical: spacing['3xl'] }}>
              <Icon
                name={state.checkout.status === 'succeeded' ? 'checkCircle' : 'closeCircle'}
                size={48}
                color={state.checkout.status === 'succeeded' ? colors.success : colors.danger}
              />
              <Text variant="title2" weight="700" align="center">
                {DONE_LABEL[state.checkout.status]}
              </Text>
              <Card style={{ width: '100%' }}>
                <SummaryCard>
                  <SummaryRow label="Paid to" value={state.checkout.merchantName} />
                  <SummaryRow label="Amount" value={formatKobo(String(state.checkout.customerTotal))} />
                </SummaryCard>
              </Card>
              {returnUrlFor(state.checkout) && (
                <Button
                  title={`Return to ${state.checkout.merchantName}`}
                  size="lg"
                  onPress={handleReturnToStore}
                />
              )}
            </View>
          )}
        </ScrollView>

        {state.kind === 'payable' && state.checkout.mode === 'test' && (
          <BottomActionBar>
            <Button
              title="Approve"
              size="lg"
              onPress={() => payTest.request('approve')}
              loading={payTest.submitting}
              disabled={payTest.submitting}
            />
            <Button
              title="Decline"
              variant="ghost"
              onPress={() => payTest.request('decline')}
              disabled={payTest.submitting}
            />
          </BottomActionBar>
        )}
      </SafeAreaView>
      <AuthorizationModal {...payTest.authorization} />
    </>
  );
}
