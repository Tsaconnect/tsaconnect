// components/p2p/PmPayoutFeeNotice.tsx
//
// The payout fees a payment merchant pays under central inbound, shown before
// they post an ad (create-ad, merchant dashboard) so they can price for them.
// Renders nothing without a schedule: VA mode forwards nothing, so charges
// nothing.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Icon, Text } from '@/components/ui';
import { describePayoutFees, type PayoutFeeSchedule } from '@/utils/pmPayoutFees';

export function PmPayoutFeeNotice({ fees }: { fees: PayoutFeeSchedule | null }) {
  const { colors, spacing } = useTheme();
  if (!fees) return null;
  return (
    <Card padding="md" style={{ borderWidth: 1, borderColor: colors.border }}>
      <View style={{ flexDirection: 'row', gap: spacing.sm }}>
        <Icon name="info" size={20} color={colors.info} />
        <View style={{ flex: 1 }}>
          <Text variant="bodySm" weight="700">
            Payout fees
          </Text>
          <Text variant="caption" color="textSecondary">
            {describePayoutFees(fees)} Price your ads to cover them.
          </Text>
        </View>
      </View>
    </Card>
  );
}
