// components/it/PayIntoCard.tsx
//
// Where to send the naira for an Instant Transfer buy: bank, account name,
// copyable account number and — in central mode — the exact amount and the
// own-name notice. Rendered by the buy screen right after an order opens and by
// the order detail screen when the user comes back to pay, from the same
// `payTo` card the API returns for both, so the two can never disagree about
// where the money goes.
//
// Central vs virtual-account mode is read off the card itself (utils/payInto):
// the "any amount, permanent account" copy is false in central mode and would
// contradict the instruction beside it.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { CopyableField, SummaryCard, SummaryRow, Text } from '@/components/ui';
import { PayerInstructionNotice } from '@/components/common/PayerInstructionNotice';
import type { ITPayTo } from '@/services/itApi';
import { exactNairaOf, payerInstructionOf } from '@/utils/payInto';

export interface PayIntoCardProps {
  payTo: ITPayTo;
}

export const PayIntoCard: React.FC<PayIntoCardProps> = ({ payTo }) => {
  const { colors, spacing } = useTheme();
  const payerInstruction = payerInstructionOf(payTo);
  const exactNaira = exactNairaOf(payTo);

  return (
    <View testID="it-pay-into-card">
      <Text
        variant="bodySm"
        weight="600"
        style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
      >
        Pay into this account
      </Text>
      {payerInstruction ? (
        <View style={{ marginBottom: spacing.sm }}>
          <PayerInstructionNotice instruction={payerInstruction} />
        </View>
      ) : null}
      <SummaryCard>
        <SummaryRow label="Bank" value={payTo.bank || '—'} />
        <SummaryRow label="Account name" value={payTo.accountName || '—'} />
        {exactNaira ? (
          <SummaryRow label="Amount (exactly)" value={`₦${exactNaira}`} />
        ) : null}
      </SummaryCard>
      <View style={{ marginTop: spacing.xs }}>
        <Text
          variant="bodySm"
          weight="600"
          style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
        >
          Account number
        </Text>
        <CopyableField value={payTo.accountNumber || ''} />
      </View>
      {payerInstruction ? null : (
        <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.md }}>
          This is your permanent Instant Transfer account — no reference to type, no expiry. Transfer
          any Naira amount; your spendable MCGP appears when the payment is confirmed, priced at the
          live rate and net of transfer fees.
        </Text>
      )}
    </View>
  );
};

export default PayIntoCard;
