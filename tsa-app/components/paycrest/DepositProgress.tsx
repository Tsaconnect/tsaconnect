// components/paycrest/DepositProgress.tsx
//
// What the sell leg shows in place of the deposit address once the app is
// sending, or has sent, the transfer from the wallet on this device.
//
// Shared by the sell screen (in flight) and the order screen (after the
// handoff) so the user sees one continuous state across the navigation rather
// than a spinner that becomes an address card.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Card, CopyableField, Spinner, Text } from '@/components/ui';

export interface DepositProgressProps {
  /** "136.269100 USDT" — what is being sent. */
  amountLabel: string;
  /** "BNB Smart Chain". */
  chainLabel: string;
  /** Present once broadcast. Absent means still signing/submitting. */
  txHash?: string;
}

export const DepositProgress: React.FC<DepositProgressProps> = ({
  amountLabel,
  chainLabel,
  txHash,
}) => {
  const { colors, spacing } = useTheme();
  const sent = !!txHash;

  return (
    <Card>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
        <Spinner size="small" />
        <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
          {sent ? 'Sent from your wallet' : 'Sending from your wallet'}
        </Text>
      </View>

      <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
        {sent
          ? `${amountLabel} is on its way on ${chainLabel}. This confirms on its own — you don't need to send anything.`
          : `Sending ${amountLabel} on ${chainLabel}. Keep this screen open.`}
      </Text>

      {/* The hash is the user's own receipt: it is the one thing that proves
          the transfer exists while the provider has not acknowledged it yet. */}
      {sent && (
        <View style={{ marginTop: spacing.sm }}>
          <CopyableField label="Transaction" value={txHash} />
        </View>
      )}
    </Card>
  );
};
