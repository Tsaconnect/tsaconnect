// Self-service account deletion (Apple guideline 5.1.1(v)).
//
// Settings previously showed a "Coming Soon" alert here. The flow now: explain
// exactly what deletion does and does not do (the wallet is non-custodial —
// deleting the account does NOT touch the user's funds), re-authenticate by
// password, then call the backend, which soft-deletes immediately and notifies
// admin ops. On success we sign the user fully out — their session is dead
// server-side anyway, so leaving them on a logged-in screen would just produce
// 403s.
import React, { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router } from 'expo-router';
import { useAuth } from '@/AuthContext/AuthContext';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Card,
  Icon,
  Input,
  Text,
  useToast,
} from '@/components/ui';
import { requestAccountDeletion } from '@/services/accountApi';
import { COMPANY } from '@/constants/legal';

export default function DeleteAccountScreen() {
  const { colors, spacing } = useTheme();
  const { logOutFull } = useAuth();
  const toast = useToast();

  const [password, setPassword] = useState('');
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [blockers, setBlockers] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const handleDelete = async () => {
    if (!password.trim()) {
      setError('Enter your password to confirm.');
      return;
    }
    setSubmitting(true);
    setError(null);
    setBlockers(null);

    const res = await requestAccountDeletion(password, reason.trim());

    if (res.ok) {
      toast.success('Your account has been deleted');
      await logOutFull();
      router.replace('/login');
      return;
    }

    setSubmitting(false);
    if (res.blockers?.length) {
      setBlockers(res.blockers);
      setError(null);
    } else {
      setError(res.message);
    }
  };

  return (
    <>
      <Stack.Screen options={{ title: 'Delete Account' }} />
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
          <ScrollView
            contentContainerStyle={{ padding: spacing.xl, paddingBottom: spacing['4xl'] }}
            keyboardShouldPersistTaps="handled"
            showsVerticalScrollIndicator={false}
          >
            <Text variant="title2" weight="700">
              Delete your account
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm }}>
              This closes your TSA Connect account straight away and signs you out of every
              device. It cannot be undone from inside the app.
            </Text>

            {/* The single most important thing to say on this screen: a
                non-custodial wallet survives account deletion. Users who think
                otherwise will not delete, or will panic afterwards. */}
            <AccentCard style={{ marginTop: spacing.lg }}>
              <View style={{ flexDirection: 'row', gap: spacing.md }}>
                <Icon name="shield" size={20} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodySm" weight="700">
                    Your crypto is not affected
                  </Text>
                  <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.xs }}>
                    Your wallet is non-custodial. Deleting your account does not move or delete
                    your funds — your recovery phrase still controls them. Make sure you have it
                    backed up before you continue.
                  </Text>
                </View>
              </View>
            </AccentCard>

            <Card padding="md" style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="700" style={{ marginBottom: spacing.sm }}>
                What happens next
              </Text>
              {[
                'You are signed out and can no longer sign in.',
                'Your listings are withdrawn and your devices stop receiving notifications.',
                'We keep a limited record for 30 days in case you ask us to reverse this.',
                'Identity-verification and completed-transaction records are kept for 5 years, because anti-money-laundering law requires it.',
              ].map((line) => (
                <View
                  key={line.slice(0, 30)}
                  style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.xs }}
                >
                  <Text variant="caption" color="accent">
                    •
                  </Text>
                  <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                    {line}
                  </Text>
                </View>
              ))}
            </Card>

            {blockers && (
              <Card padding="md" style={{ marginTop: spacing.md, borderColor: colors.danger, borderWidth: 1 }}>
                <Text variant="bodySm" weight="700" color="danger">
                  Settle these first
                </Text>
                <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.xs }}>
                  You still have commitments to other users. Finish or cancel them, then come
                  back.
                </Text>
                {blockers.map((b) => (
                  <Text key={b} variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                    • {b}
                  </Text>
                ))}
              </Card>
            )}

            <View style={{ marginTop: spacing.xl, gap: spacing.md }}>
              <Input
                label="Confirm your password"
                value={password}
                onChangeText={(v) => {
                  setPassword(v);
                  setError(null);
                }}
                secureTextEntry
                autoCapitalize="none"
                autoComplete="current-password"
                textContentType="password"
                placeholder="Your account password"
                error={error ?? undefined}
              />
              <Input
                label="Why are you leaving? (optional)"
                value={reason}
                onChangeText={setReason}
                placeholder="This helps us improve"
                multiline
                numberOfLines={3}
                maxLength={500}
                counter
              />
            </View>

            <View style={{ marginTop: spacing.xl, gap: spacing.sm }}>
              <Button
                title="Delete my account"
                variant="danger"
                loading={submitting}
                disabled={submitting || !password.trim()}
                onPress={handleDelete}
                fullWidth
              />
              <Button
                title="Keep my account"
                variant="ghost"
                disabled={submitting}
                onPress={() => router.back()}
                fullWidth
              />
            </View>

            <Text
              variant="caption"
              color="textMuted"
              style={{ marginTop: spacing.lg, textAlign: 'center' }}
            >
              Changed your mind after deleting? Email {COMPANY.supportEmail} within 30 days.
            </Text>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </>
  );
}
