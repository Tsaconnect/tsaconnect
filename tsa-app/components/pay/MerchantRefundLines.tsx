// components/pay/MerchantRefundLines.tsx
//
// The rows a payer's done summary gains once the merchant refunds her: what came back
// (and when), or that one is in progress, and the transaction on its explorer.
import React from 'react';
import { Linking } from 'react-native';
import { SummaryRow, Text } from '@/components/ui';
import { txExplorerUrl } from '@/constants/chains';
import type { MerchantRefund } from '@/services/payApi';
import { merchantRefundLine } from '@/utils/payRefunds';
import { shortAddress } from '@/utils/sendForm';

export function MerchantRefundLines({ refund }: { refund: MerchantRefund }) {
  const url = refund.txHash && refund.chainId ? txExplorerUrl(refund.txHash, { chainId: refund.chainId }) : undefined;
  const label = refund.status === 'pending' ? 'Refund' : refund.status === 'partial' ? 'Partly refunded' : 'Refunded';
  return (
    <>
      <SummaryRow label={label} value={refund.status === 'pending' ? 'In progress' : merchantRefundLine(refund)} />
      {url && refund.txHash ? (
        <SummaryRow
          label="Refund transaction"
          value={
            <Text variant="bodySm" color="accent" accessibilityRole="link" accessibilityLabel="View refund transaction" onPress={() => void Linking.openURL(url)}>
              {shortAddress(refund.txHash)}
            </Text>
          }
        />
      ) : null}
    </>
  );
}
