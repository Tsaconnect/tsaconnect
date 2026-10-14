// The TSA Liquidity entry card: opens the liquidity screen, which offers
// Activate to anyone not yet an LP. Shared by the profile and the merchant
// dashboard; lpCta decides whether it shows.
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { AccentCard, Icon, PressableScale, Text } from '@/components/ui';
import { lpCta, type LpCtaInput } from '@/utils/lpCta';

export function LpCtaCard(props: LpCtaInput) {
  const { colors, spacing } = useTheme();
  const cta = lpCta(props);
  if (!cta) return null;
  return (
    <PressableScale onPress={() => router.push(cta.route as never)} style={{ marginBottom: spacing.lg }}>
      <AccentCard padding="md">
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <Icon name="wallet" size={24} color={colors.accent} />
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="700">{cta.title}</Text>
            <Text variant="caption" color="textSecondary">{cta.subtitle}</Text>
          </View>
          <Icon name="chevronRight" size={20} color={colors.textMuted} />
        </View>
      </AccentCard>
    </PressableScale>
  );
}
