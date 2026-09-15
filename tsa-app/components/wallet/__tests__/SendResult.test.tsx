/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * The Send receipt's three states. The rules worth pinning: only a confirmed
 * send says "sent" and offers Send again; an unconfirmed one says it is on its
 * way and warns against sending twice; a failure shows why and offers a retry.
 */
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('expo-clipboard', () => ({ __esModule: true, setStringAsync: jest.fn(async () => true) }));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: {
      background: '#fff', surfaceAlt: '#eee', accent: '#c90',
      success: '#0f0', successBg: '#efe', warning: '#fa0', warningBg: '#ffe', danger: '#f00', dangerBg: '#fee',
    },
    spacing: { xs: 4, sm: 8, md: 12, lg: 16, '3xl': 32 },
    radius: { md: 12, pill: 999 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText, Pressable } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  const pressable = (props: any, children: any) =>
    react.createElement(Pressable, { onPress: props.onPress, accessibilityLabel: props.accessibilityLabel }, children);
  return {
    __esModule: true,
    Text: (props: any) => label(props.children),
    Badge: (props: any) => label(props.label),
    Button: (props: any) => pressable({ ...props, accessibilityLabel: props.title }, label(props.title)),
    PressableScale: (props: any) => pressable(props, props.children),
    Card: box,
    SummaryCard: box,
    BottomActionBar: box,
    Icon: () => null,
    SummaryRow: (props: any) =>
      react.createElement(View, null, label(props.label), typeof props.value === 'string' ? label(props.value) : props.value),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as Clipboard from 'expo-clipboard';
import SendResult, { type SendResultProps } from '../SendResult';

const base: SendResultProps = {
  status: 'success',
  amountLabel: '10 USDT',
  recipient: '0x7a3F000000000000000000000000000000c91E',
  usdValue: '10.00',
  networkName: 'BNB Smart Chain',
  feeLabel: '0.0000058 BNB',
  sentAt: null,
  txHash: '0x4be1c07d',
  explorerUrl: 'https://bscscan.com/tx/0x4be1c07d',
  error: '',
  onDone: jest.fn(),
  onSendAgain: jest.fn(),
  onTryAgain: jest.fn(),
};

function render(overrides: Partial<SendResultProps> = {}): ReactTestRenderer {
  let tree: ReactTestRenderer | undefined;
  act(() => {
    tree = TestRenderer.create(<SendResult {...base} {...overrides} />);
  });
  return tree!;
}

/** Everything on screen, as text — the same check the other screen tests use. */
const screenText = (tree: ReactTestRenderer) => JSON.stringify(tree.toJSON());

/** The pressable behind a Button or PressableScale, by its label. */
const pressable = (tree: ReactTestRenderer, label: string) =>
  tree.root.findAll(
    (node) => node.props.accessibilityLabel === label && typeof node.props.onPress === 'function',
  )[0];

beforeEach(() => jest.clearAllMocks());

describe('SendResult', () => {
  it('confirmed: says sent, shows the receipt, offers Send again', () => {
    const tree = render();
    const shown = screenText(tree);
    for (const text of ['10 USDT sent', '0x7a3F…c91E', 'Confirmed', 'BNB Smart Chain', '0.0000058 BNB']) {
      expect(shown).toContain(text);
    }

    act(() => pressable(tree, 'Send again').props.onPress());
    act(() => pressable(tree, 'Done').props.onPress());
    expect(base.onSendAgain).toHaveBeenCalledTimes(1);
    expect(base.onDone).toHaveBeenCalledTimes(1);
  });

  it('unconfirmed: says on its way, warns against a second send, no Send again', () => {
    const tree = render({ status: 'pending' });
    const shown = screenText(tree);
    expect(shown).toContain('Sending 10 USDT');
    expect(shown).toContain('On its way');
    expect(shown).toContain('Don’t send it again');
    expect(shown).not.toContain('10 USDT sent');
    expect(pressable(tree, 'Send again')).toBeUndefined();
  });

  it('failed: shows the reason and offers a retry', () => {
    const tree = render({ status: 'failure', error: 'Not enough native token to pay gas on this chain.' });
    const shown = screenText(tree);
    expect(shown).toContain('Send failed');
    expect(shown).toContain('Not enough native token to pay gas on this chain.');
    expect(shown).not.toContain('Confirmed');

    act(() => pressable(tree, 'Try again').props.onPress());
    expect(base.onTryAgain).toHaveBeenCalledTimes(1);
  });

  it('failed without a reason: says the funds have not moved', () => {
    expect(screenText(render({ status: 'failure', error: '' }))).toContain(
      'Something went wrong. Your funds have not moved.',
    );
  });

  it('copies the transaction hash', async () => {
    const tree = render();
    await act(async () => {
      await pressable(tree, 'Copy transaction hash').props.onPress();
    });
    expect(Clipboard.setStringAsync).toHaveBeenCalledWith('0x4be1c07d');
    act(() => tree.unmount());
  });
});
