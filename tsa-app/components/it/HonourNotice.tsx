// components/it/HonourNotice.tsx
//
// The rate hold on a routed stablecoin buy (utils/rampHonour): a countdown
// while her quote is held, what happens once it ends, and the "Rate updated"
// notice when a late transfer was priced at the live rate. Names no provider.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { useCountdown } from '@/hooks/useCountdown';

export interface HonourNoticeProps {
  honourUntil?: string;
  /** Still waiting for her transfer. */
  pending: boolean;
  rateUpdated: boolean;
  /** What she receives, formatted ("96 USDC"). */
  receive: string;
}

export const HonourNotice: React.FC<HonourNoticeProps> = ({ honourUntil, pending, rateUpdated, receive }) => {
  const { colors, spacing } = useTheme();
  const { label, expired } = useCountdown(honourUntil);

  if (rateUpdated && !pending) {
    return (
      <View testID="it-honour-rate-updated" style={{ marginTop: spacing.md }}>
        <Text variant="bodySm" weight="600" style={{ color: colors.warning }}>
          {`Rate updated: you'll receive ${receive}`}
        </Text>
        <Text variant="caption" style={{ color: colors.textMuted }}>
          Your transfer arrived after your rate hold ended, or for a different amount, so it was priced at the rate when it landed.
        </Text>
      </View>
    );
  }
  if (!pending || !honourUntil) return null;
  if (expired) {
    return (
      <View testID="it-honour-ended" style={{ marginTop: spacing.md }}>
        <Text variant="bodySm" weight="600" style={{ color: colors.warning }}>
          Your rate hold has ended
        </Text>
        <Text variant="caption" style={{ color: colors.textMuted }}>
          If you pay now, you&apos;ll get the rate when your transfer arrives, not the one shown.
        </Text>
      </View>
    );
  }
  return (
    <View testID="it-honour-countdown" style={{ marginTop: spacing.md }}>
      <Text variant="bodySm" style={{ color: colors.textSecondary }}>
        Rate held for
      </Text>
      <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
        {label}
      </Text>
      <Text variant="caption" style={{ color: colors.textMuted }}>
        {`Pay the exact amount within this time to get exactly ${receive}.`}
      </Text>
    </View>
  );
};
