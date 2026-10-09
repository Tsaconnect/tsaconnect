/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, { onPress: props.onPress, accessibilityLabel: props.accessibilityLabel }, props.children),
    SummaryRow: (props: any) => (typeof props.value === 'string'
      ? react.createElement(RNText, null, `${props.label}: ${props.value}`)
      : react.createElement(View, null, react.createElement(RNText, null, `${props.label}:`), props.value)),
  };
});

import React from 'react';
import { Linking } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { MerchantRefundLines } from '../MerchantRefundLines';

const HASH = `0x${'ab'.repeat(32)}`;
const textOf = (t: ReactTestRenderer) => JSON.stringify(t.toJSON());
// A LOCAL date, so the day reads the same in every timezone.
const OCT_9 = new Date(2026, 9, 9, 12).toISOString();

it('shows what came back, when, and opens the transaction', async () => {
  const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
  let t!: ReactTestRenderer;
  await act(async () => {
    t = TestRenderer.create(<MerchantRefundLines refund={{ status: 'full', amount: '12.50', tokenSymbol: 'USDT', chainId: 56, txHash: HASH, confirmedAt: OCT_9 }} />);
  });
  expect(textOf(t)).toContain('Refunded: 12.50 USDT · 9 Oct');
  await act(async () => { t.root.findAll((n: any) => n.props.accessibilityLabel === 'View refund transaction')[0].props.onPress(); });
  expect(open).toHaveBeenCalledWith(`https://bscscan.com/tx/${HASH}`);
});

it('a partial refund and a pending one say so, with no link before there is a transaction', async () => {
  let t!: ReactTestRenderer;
  await act(async () => { t = TestRenderer.create(<MerchantRefundLines refund={{ status: 'partial', amount: '5.00', tokenSymbol: 'USDC' }} />); });
  expect(textOf(t)).toContain('Partly refunded: 5.00 USDC');
  await act(async () => { t.update(<MerchantRefundLines refund={{ status: 'pending', amount: '0.00' }} />); });
  expect(textOf(t)).toContain('Refund: In progress');
  expect(t.root.findAll((n: any) => n.props.accessibilityLabel === 'View refund transaction')).toHaveLength(0);
});
