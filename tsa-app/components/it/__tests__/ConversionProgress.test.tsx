/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000' }), spacing: new Proxy({}, { get: () => 8 }) }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(View, { title: props.title, onPress: props.onPress }),
    Stepper: (props: any) =>
      react.createElement(View, null, props.steps.map((h: any) => react.createElement(RNText, { key: h.key }, h.label))),
  };
});
import React from 'react';
import { act } from 'react-test-renderer';
import { router } from 'expo-router';
import { ConversionProgress } from '../ConversionProgress';
import type { ITConversion } from '@/services/itApi';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');

const conversion: ITConversion = {
  orderId: 'rec1', status: 'converting', holder: 'company_wallet', requestedToken: 'USDT', requestedChain: 'optimism',
  requestedChainId: 10, requestedTokenDecimals: 6, estimatedAmount: '7190000', promisedAmount: '7196772', markupBps: 50,
  walletAddress: '0xUser',
};

async function render(el: React.ReactElement): Promise<any> {
  let t: any;
  await act(async () => { t = TestRenderer.create(el); });
  return t;
}

const text = (t: any) => JSON.stringify(t.toJSON());

describe('ConversionProgress', () => {
  it('says what she receives and where, with the conversion hops', async () => {
    const t = await render(<ConversionProgress conversion={conversion} paid />);
    expect(text(t)).toContain('USDT');
    expect(text(t)).toContain('Converting');
  });
  it('tracks the delivery on the converted-order screen once paid', async () => {
    const t = await render(<ConversionProgress conversion={conversion} paid />);
    const track = t.root.findByProps({ testID: 'it-conversion-track' });
    act(() => track.findByProps({ title: 'Track delivery' }).props.onPress());
    expect(router.push).toHaveBeenCalledWith('/wallet/paycrest/order/rec1');
  });
  it('offers no tracking before payment', async () => {
    const t = await render(<ConversionProgress conversion={{ ...conversion, status: 'awaiting_payment' }} paid={false} />);
    expect(t.root.findAllByProps({ testID: 'it-conversion-track' })).toHaveLength(0);
  });
});
