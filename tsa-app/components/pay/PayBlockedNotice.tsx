// components/pay/PayBlockedNotice.tsx
//
// Why app/pay.tsx won't let her pay. A block she can clear herself (identity
// verification) reads as a next step, not an error; one only support can clear
// says so. Keyed on the server's code, never its words (utils/kycRefusal.ts).
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { AccentCard, Button, Icon, Text } from '@/components/ui';
import { emailSupport } from '@/utils/contactSupport';
import { KYC_LEGAL_NAME_MISSING, KYC_VERIFICATION_REQUIRED } from '@/utils/kycRefusal';

export function PayBlockedNotice({ why, code }: { why: string; code?: string }) {
  const { colors, spacing } = useTheme();

  if (code === KYC_VERIFICATION_REQUIRED) {
    return (
      <AccentCard railColor={colors.accent} padding="md">
        <View style={{ gap: spacing.sm }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Icon name="shield" size={20} color={colors.accent} />
            <Text variant="body" weight="700">Verify your identity to pay</Text>
          </View>
          <Text variant="bodySm" color="textSecondary">
            Payments with real money need a verified account. It usually takes a few minutes, and we&apos;ll bring
            you back to this payment when you&apos;re done.
          </Text>
        </View>
      </AccentCard>
    );
  }

  if (code === KYC_LEGAL_NAME_MISSING) {
    return (
      <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg }}>
        <View style={{ gap: spacing.sm }}>
          <Text variant="body" weight="700">Contact support to pay</Text>
          <Text variant="bodySm">{why}</Text>
          <Button title="Email support" variant="secondary" onPress={() => emailSupport('My verified name is missing')} />
        </View>
      </AccentCard>
    );
  }

  return (
    <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg }}>
      <Text variant="bodySm" color="danger">
        {why}
      </Text>
    </AccentCard>
  );
}
