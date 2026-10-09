import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { AmountText, Card, CopyableField, Text } from '@/components/ui';
import { PayerInstructionNotice } from '@/components/common/PayerInstructionNotice';
import { payToCopy, payerInstructionOf, type PayToDetails, type PayToMode } from '@/utils/payInto';

// Pay-to card shared by PM (automated) and direct P2P trades, for payer and payee.
export const PayToCard: React.FC<{
  details: PayToDetails;
  amount: string;
  currency: string;
  mode: PayToMode;
  payerName?: string;
}> = ({ details, amount, currency, mode, payerName }) => {
  const { colors, spacing } = useTheme();
  const { heading, note } = payToCopy(mode, payerName);
  // Central mode: the card points at the shared company account and the API
  // supplies the own-name notice. The local PM copy predates it and is only
  // right for a merchant's own account — in central mode a mismatched name does
  // not reliably route to review (a partial match can credit another order),
  // and payerName is the free-text signup name, not the verified name matched.
  const instruction = mode === 'pm' ? payerInstructionOf(details) : null;
  return (
    <Card style={{ gap: spacing.md }}>
      <View style={{ alignItems: 'center', gap: spacing.xs }}>
        <Text variant="bodySm" color="textSecondary">{heading}</Text>
        <AmountText value={amount} currency={currency} variant="title2" />
      </View>
      {instruction ? (
        <>
          <PayerInstructionNotice instruction={instruction} />
          <Text variant="bodySm" color="textSecondary">
            We release your crypto automatically once your transfer lands — no further action needed.
          </Text>
        </>
      ) : (
        <Text variant="bodySm" weight="600" style={{ color: mode === 'pm' ? colors.danger : colors.textSecondary }}>
          {note}
        </Text>
      )}
      <View style={{ gap: spacing.sm }}>
        <CopyableField label="Account number" value={details.accountNumber} toastMessage="Account number copied" />
        <CopyableField label="Bank" value={details.bankLabel} toastMessage="Bank copied" />
        <CopyableField label="Account name" value={details.accountName} toastMessage="Account name copied" />
      </View>
    </Card>
  );
};
