/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * The pay-into card shared by the buy screen and the order detail. In central
 * mode it must carry the exact amount and the own-name notice; in VA mode it
 * must not, and must say any amount is fine.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: { textSecondary: '#555', textMuted: '#777' },
    spacing: { xs: 4, sm: 8, md: 12 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text, View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(Text, null, props.children),
    SummaryCard: (props: any) => react.createElement(View, null, props.children),
    SummaryRow: (props: any) => react.createElement(Text, null, `${props.label}: ${props.value}`),
    CopyableField: (props: any) => react.createElement(Text, null, `copy:${props.value}`),
  };
});

jest.mock('@/components/common/PayerInstructionNotice', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    PayerInstructionNotice: (props: any) => react.createElement(Text, null, `notice:${props.instruction}`),
  };
});

import React from 'react';
import { Text } from 'react-native';
import { PayIntoCard } from '../PayIntoCard';
import type { ITPayTo } from '@/services/itApi';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');

function texts(payTo: ITPayTo): string[] {
  let tree: any;
  TestRenderer.act(() => {
    tree = TestRenderer.create(<PayIntoCard payTo={payTo} />);
  });
  return tree.root.findAllByType(Text).map((node: any) => String(node.props.children));
}

describe('PayIntoCard', () => {
  it('central mode: company account, exact amount, own-name notice', () => {
    const out = texts({
      accountNumber: '0123456789',
      bank: 'Providus Bank',
      accountName: 'TSA CONNECT LTD',
      amountNgn: '12500',
      payerInstruction: 'Pay from a bank account named JOHN DOE.',
    });
    expect(out).toContain('Bank: Providus Bank');
    expect(out).toContain('Account name: TSA CONNECT LTD');
    expect(out).toContain('Amount (exactly): ₦12,500');
    expect(out).toContain('copy:0123456789');
    expect(out).toContain('notice:Pay from a bank account named JOHN DOE.');
    expect(out.join('\n')).not.toMatch(/Transfer any Naira amount|permanent/);
    // No caption at all — not even an empty one holding space under the card.
    expect(out).not.toContain('');
  });

  it('VA mode: the user’s own account, no amount, no notice', () => {
    const out = texts({ accountNumber: '9988776655', bank: 'Wema Bank', accountName: 'JOHN DOE' });
    expect(out).toContain('copy:9988776655');
    expect(out.some((t) => t.startsWith('Amount'))).toBe(false);
    expect(out.some((t) => t.startsWith('notice:'))).toBe(false);
    expect(out.join('\n')).toMatch(/Transfer any Naira amount/);
  });
});
