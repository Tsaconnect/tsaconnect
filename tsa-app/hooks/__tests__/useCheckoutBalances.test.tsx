/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The marketplace escrow is on Sonic MAINNET. A tester on the testnet toggle
 * used to see testnet USDC here — money that cannot pay — while escrow ran on
 * mainnet. The balances are read from chain 146, network=mainnet, whatever the
 * toggle says.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/hooks/useNetwork', () => ({ __esModule: true, getNetwork: () => 'testnet' }));

jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => ({
    success: true,
    data: { balances: { sonic: { USDC: { balance: '12.5' }, USDT: { balance: '0' }, S: { balance: '1' } } } },
  })),
  normalizeWalletBalances: jest.requireActual('@/services/walletApi').normalizeWalletBalances,
}));

jest.mock('@/services/mcgpSwapApi', () => ({ __esModule: true, getMcgpBalance: jest.fn(async () => ({ success: false })) }));

jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getSignableWallet: jest.fn(async () => '0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce'),
}));

jest.mock('@/services/serviceCheckout', () => ({ __esModule: true, MARKETPLACE_NETWORK: 'mainnet' }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getWalletBalances } from '@/services/walletApi';
import { CHECKOUT_CHAIN_ID, useCheckoutBalances } from '../useCheckoutBalances';

let result: ReturnType<typeof useCheckoutBalances>;
const Harness = () => {
  result = useCheckoutBalances();
  return null;
};

test('reads Sonic mainnet (146, network=mainnet) even with the testnet toggle on', async () => {
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(<Harness />);
  });
  expect(CHECKOUT_CHAIN_ID).toBe(146);
  expect(getWalletBalances).toHaveBeenCalledWith(146, '0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce', 'mainnet');
  expect(result.address).toBe('0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce');
  act(() => renderer.unmount());
});
