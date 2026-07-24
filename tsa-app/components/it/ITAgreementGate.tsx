// components/it/ITAgreementGate.tsx
//
// The Instant Transfer trading-agreement gate. Rendered by the IT hub when the
// user has not accepted the current agreement version. "I Agree" records the
// acceptance (backend audit trail) and calls onAccepted so the hub reloads;
// "Cancel" calls onCancel (the hub sends the user back — no IT trading until
// they agree). The backend independently enforces the same gate on every trade,
// so this screen is the UX, not the security boundary.
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Button, Card, Spinner, Text } from '@/components/ui';
import { acceptITAgreement, getITAgreement, type ITAgreement } from '@/services/itApi';
import api from '@/components/services/api';
import { agreementGreeting } from '@/utils/agreementCopy';

interface Props {
  onAccepted: () => void;
  onCancel: () => void;
}

export default function ITAgreementGate({ onAccepted, onCancel }: Props) {
  const { colors, spacing } = useTheme();
  const [agreement, setAgreement] = useState<ITAgreement | null>(null);
  const [greeting, setGreeting] = useState('Hello,');
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    const res = await getITAgreement();
    if (!res.success || !res.data) {
      setError(res.message || 'Could not load the agreement');
      setLoading(false);
      return;
    }
    setAgreement(res.data);
    // Already accepted (e.g. accepted on another device) — let the hub through.
    if (res.data.accepted) {
      onAccepted();
      return;
    }
    // Personalize best-effort; a missing name just yields "Hello,".
    try {
      const profile = await api.getProfile();
      setGreeting(agreementGreeting(profile?.data?.name));
    } catch {
      setGreeting(agreementGreeting(null));
    }
    setLoading(false);
  }, [onAccepted]);

  useEffect(() => {
    load();
  }, [load]);

  const agree = async () => {
    setSubmitting(true);
    setError('');
    const res = await acceptITAgreement();
    if (!res.success) {
      setError(res.message || 'Could not record your acceptance');
      setSubmitting(false);
      return;
    }
    onAccepted();
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
          {agreement?.title || 'User Trading Agreement'}
        </Text>
        <Text variant="body" style={{ color: colors.textPrimary }}>{greeting}</Text>
        {!!agreement?.intro && (
          <Text variant="bodySm" style={{ color: colors.textSecondary }}>{agreement.intro}</Text>
        )}

        <Card style={{ padding: spacing.md, gap: spacing.md }}>
          {(agreement?.clauses ?? []).map((clause, i) => (
            <View key={i} style={{ flexDirection: 'row', gap: spacing.sm }}>
              <Text variant="bodySm" weight="700" style={{ color: colors.textPrimary }}>{i + 1}.</Text>
              <Text variant="bodySm" style={{ color: colors.textPrimary, flex: 1 }}>{clause}</Text>
            </View>
          ))}
        </Card>

        {!!error && (
          <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text>
        )}

        <View style={{ gap: spacing.sm }}>
          <Button title="I Agree" onPress={agree} loading={submitting} />
          <Button title="Cancel" variant="secondary" onPress={onCancel} disabled={submitting} />
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}
