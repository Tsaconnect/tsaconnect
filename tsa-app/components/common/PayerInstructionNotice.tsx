// components/common/PayerInstructionNotice.tsx
//
// The warning shown beside the shared company account on every naira pay-in
// screen. Payments into that account are credited by matching the sender's
// bank-account name, so a transfer from someone else's account can land on the
// wrong order — telling the payer is the mitigation that risk was accepted on.
// The recipient's registered name is also not "TSA Connect", and a payer who
// meets it unexplained reads the transfer as a scam.
//
// The wording is the API's (CentralAccount.PayerInstruction), rendered verbatim
// so it can change without an app release and cannot drift between products.
import React from 'react';
import { View } from 'react-native';
import { Icon, Text } from '@/components/ui';
import { useTheme } from '@/theme';

export interface PayerInstructionNoticeProps {
  instruction: string;
}

export const PayerInstructionNotice: React.FC<PayerInstructionNoticeProps> = ({ instruction }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      testID="payer-instruction-notice"
      accessible
      accessibilityRole="alert"
      accessibilityLabel={`Before you transfer. ${instruction}`}
      style={{
        flexDirection: 'row',
        gap: spacing.sm,
        padding: spacing.md,
        borderRadius: radius.md,
        borderWidth: 1,
        borderColor: colors.warning,
        backgroundColor: colors.warningBg,
      }}
    >
      <Icon name="warning" size={20} color={colors.warning} />
      <View style={{ flex: 1, gap: spacing.xs }}>
        <Text variant="bodySm" weight="700">
          Before you transfer
        </Text>
        <Text variant="bodySm">{instruction}</Text>
      </View>
    </View>
  );
};

export default PayerInstructionNotice;
