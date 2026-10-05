/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The payout-fee notice a payment merchant sees before posting an ad (create-ad
 * and the merchant dashboard). It renders the server's schedule, and nothing at
 * all when there is none — VA mode forwards nothing, so charges nothing.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Card: (props: any) => react.createElement(View, null, props.children),
    Icon: () => null,
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { PmPayoutFeeNotice } from '@/components/p2p/PmPayoutFeeNotice';
import { describePayoutFees } from '@/utils/pmPayoutFees';

const fees = {
  fundingFeeBps: 140,
  transferFeeTiers: [{ upToNgn: null, feeNgn: '53.75' }],
  minPayoutNgn: '100.00',
};

function textOf(tree: ReactTestRenderer): string {
  return JSON.stringify(tree.toJSON());
}

describe('PmPayoutFeeNotice', () => {
  it('shows the schedule the server deducts', async () => {
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(<PmPayoutFeeNotice fees={fees} />);
    });
    expect(textOf(tree)).toContain(describePayoutFees(fees));
  });

  it('renders nothing without a schedule', async () => {
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(<PmPayoutFeeNotice fees={null} />);
    });
    expect(tree.toJSON()).toBeNull();
  });
});
