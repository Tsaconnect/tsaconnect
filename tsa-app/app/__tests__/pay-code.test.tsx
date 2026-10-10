/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * TSA Pay, the payer's hub: she types or pastes the code a business gave her
 * (or pastes its pay link) and lands on the ordinary checkout. The code is
 * forgiven how she typed it; a wrong one, or too many tries, says so in her
 * words. Below it: payments she opened and has not paid, her recent payments,
 * and — for a TSA Pay merchant — the way to get paid.
 */
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn() },
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(() => cb(), [cb]),
  };
});
jest.mock('expo-clipboard', () => ({ __esModule: true, getStringAsync: jest.fn(async () => '') }));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000000' }), spacing: new Proxy({}, { get: () => 8 }) }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText, TextInput, View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(RNText, { testID: `button:${props.title}`, onPress: props.disabled ? undefined : props.onPress }, props.title),
    Card: (props: any) => react.createElement(View, null, props.children),
    Icon: () => null,
    PressableScale: (props: any) =>
      react.createElement(View, { testID: props.accessibilityLabel, onPress: props.onPress }, props.children),
    Input: (props: any) =>
      react.createElement(View, null,
        props.leftAdornment,
        react.createElement(TextInput, { testID: 'code', value: props.value, onChangeText: props.onChangeText }),
        props.rightAdornment,
        props.error ? react.createElement(RNText, null, props.error) : null),
  };
});
jest.mock('@/services/payApi', () => ({ __esModule: true, lookupPayCode: jest.fn(), getCheckout: jest.fn() }));
jest.mock('@/utils/recentPayCheckouts', () => ({
  __esModule: true,
  listPayCheckouts: jest.fn(async () => []),
  forgetPayCheckouts: jest.fn(async () => {}),
}));
jest.mock('@/hooks/useOwnPayMerchant', () => ({ __esModule: true, useOwnPayMerchant: jest.fn(() => ({ load: { kind: 'none' } })) }));
jest.mock('@/components/activity/ActivityFeed', () => {
  const react = jest.requireActual('react');
  return { __esModule: true, default: (props: any) => react.createElement('mock-feed', props) };
});
jest.mock('@/components/pay/PayLinkEntry', () => {
  const react = jest.requireActual('react');
  return { __esModule: true, PayLinkEntry: (props: any) => react.createElement('mock-pay-link-entry', props) };
});
jest.mock('@/utils/payLinks', () => ({ __esModule: true, formatLinkTime: (iso: string) => `at ${iso}` }));
jest.mock('@/utils/stableSellQuote', () => ({ __esModule: true, formatKobo: (kobo: string) => `NGN ${kobo}` }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as Clipboard from 'expo-clipboard';
import { router } from 'expo-router';
import { getCheckout, lookupPayCode } from '@/services/payApi';
import { forgetPayCheckouts, listPayCheckouts } from '@/utils/recentPayCheckouts';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import PayCodeScreen from '../(detail)/pay-code';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
let tree: ReactTestRenderer;
const host = (testID: string) => tree.root.findAll((n) => n.props.testID === testID && typeof (n as { type?: unknown }).type === 'string')[0];
const mount = async () => { await act(async () => { tree = TestRenderer.create(<PayCodeScreen />); }); };
const type = async (value: string) => { await act(async () => { host('code').props.onChangeText(value); }); };
const press = async (title: string) => { await act(async () => { await host(`button:${title}`).props.onPress(); }); };
const shown = () => JSON.stringify(tree.toJSON());

beforeEach(() => jest.clearAllMocks());
afterEach(() => act(() => tree?.unmount()));

it('a code typed any which way opens its checkout', async () => {
  jest.mocked(lookupPayCode).mockResolvedValue(ID);
  await mount();
  await type('tsa 7k2qf m9xwo');
  expect(host('code').props.value).toBe('7K2QF-M9XW0');
  await press('Continue');
  expect(lookupPayCode).toHaveBeenCalledWith('7K2QFM9XW0');
  expect(router.push).toHaveBeenCalledWith(`/pay?id=${ID}`);
});

// Review Focus 3: a long-press paste lands in onChangeText whole — nothing cuts it first.
it('pasting a whole TSA-… code into the field fills it with its ten characters', async () => {
  await mount();
  await type('TSA-7K2QF-M9XWD');
  expect(host('code').props.value).toBe('7K2QF-M9XWD');
});

it('Paste fills the field from the clipboard and, a whole code, opens its checkout', async () => {
  jest.mocked(lookupPayCode).mockResolvedValue(ID);
  jest.mocked(Clipboard.getStringAsync).mockResolvedValue('TSA-7K2QF-M9XWD\n');
  await mount();
  await press('Paste');
  expect(host('code').props.value).toBe('7K2QF-M9XWD');
  expect(lookupPayCode).toHaveBeenCalledWith('7K2QFM9XWD');
  expect(router.push).toHaveBeenCalledWith(`/pay?id=${ID}`);
});

it('Paste with half a code fills the field and waits', async () => {
  jest.mocked(Clipboard.getStringAsync).mockResolvedValue('7K2QF');
  await mount();
  await press('Paste');
  expect(host('code').props.value).toBe('7K2QF');
  expect(lookupPayCode).not.toHaveBeenCalled();
});

it('a pasted pay link opens its checkout without a code lookup', async () => {
  jest.mocked(Clipboard.getStringAsync).mockResolvedValue(`https://tsaconnectworld.com/pay/${ID.toUpperCase()}`);
  await mount();
  await press('Paste');
  expect(router.push).toHaveBeenCalledWith(`/pay?id=${ID}`);
  expect(lookupPayCode).not.toHaveBeenCalled();
});

it("a pasted link that isn't a one-time pay link says so", async () => {
  jest.mocked(Clipboard.getStringAsync).mockResolvedValue('https://tsaconnectworld.com/pay/l/shop-12');
  await mount();
  await press('Paste');
  expect(router.push).not.toHaveBeenCalled();
  expect(shown()).toContain("That isn't a TSA Pay code or payment link.");
});

// Review Focus 2.
it('a bare code that starts with TSA keeps all ten characters', async () => {
  jest.mocked(lookupPayCode).mockResolvedValue(ID);
  await mount();
  await type('TSA12345XY');
  expect(host('code').props.value).toBe('TSA12-345XY');
  await press('Continue');
  expect(lookupPayCode).toHaveBeenCalledWith('TSA12345XY');
});

it('Continue does nothing until the code is whole', async () => {
  await mount();
  await type('7K2QF');
  expect(host('button:Continue').props.onPress).toBeUndefined();
});

it('an unknown code says so in her words', async () => {
  jest.mocked(lookupPayCode).mockRejectedValue(Object.assign(new Error('payment code not found'), { status: 404 }));
  await mount();
  await type('TSA-7K2QF-M9XWD');
  await press('Continue');
  expect(shown()).toContain("We couldn't find that code. Check it and try again.");
  expect(router.push).not.toHaveBeenCalled();
});

it('too many tries says to wait', async () => {
  jest.mocked(lookupPayCode).mockRejectedValue(Object.assign(new Error('Too many requests'), { status: 429 }));
  await mount();
  await type('TSA-7K2QF-M9XWD');
  await press('Continue');
  expect(shown()).toContain('Too many tries — wait a minute.');
});

// Controller ruling: the leading TSA- is stripped as it is typed, one key at a time.
it('typing TSA-7K2QF-M9XWD key by key ends with the code and Continue enabled', async () => {
  await mount();
  let value = '';
  for (const ch of 'TSA-7K2QF-M9XWD') {
    await type(value + ch);
    value = host('code').props.value;
  }
  expect(host('code').props.value).toBe('7K2QF-M9XWD');
  expect(host('button:Continue').props.onPress).toBeDefined();
});

// Typed with no separator, TSA + the code runs past ten characters: the TSA was the prefix.
it('typing TSA7K2QFM9XWD key by key, no dash, ends with the code', async () => {
  await mount();
  let value = '';
  for (const ch of 'TSA7K2QFM9XWD') {
    await type(value + ch);
    value = host('code').props.value;
  }
  expect(host('code').props.value).toBe('7K2QF-M9XWD');
  expect(host('button:Continue').props.onPress).toBeDefined();
});

// Two taps (or tap + submitEditing) in the same frame fire one lookup.
it('two submits in the same frame look the code up once', async () => {
  let resolve: (id: string) => void = () => {};
  jest.mocked(lookupPayCode).mockReturnValue(new Promise<string>((r) => { resolve = r; }));
  await mount();
  await type('7K2QFM9XW0');
  await act(async () => {
    const submit = host('button:Continue').props.onPress;
    void submit();
    void submit();
  });
  expect(lookupPayCode).toHaveBeenCalledTimes(1);
  await act(async () => { resolve(ID); });
});

const OTHER = '9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d';
const remembered = (id: string, merchantName: string) => ({ id, merchantName, customerTotal: 505051, until: Date.now() + 60_000 });

it('Waiting for you lists the payments she opened and can still pay, and forgets the rest', async () => {
  jest.mocked(listPayCheckouts).mockResolvedValue([remembered(ID, 'Ojay store'), remembered(OTHER, 'Acme')]);
  jest.mocked(getCheckout).mockImplementation(async (id: string) => (id === ID
    ? { id, merchantName: 'Ojay store', customerTotal: 505051, status: 'pending', payable: true, youArePaying: false, expiresAt: '2026-10-16T11:12:00Z' }
    : { id, merchantName: 'Acme', customerTotal: 100000, status: 'succeeded', payable: false, youArePaying: false, expiresAt: '2026-10-16T11:12:00Z' }) as any);
  await mount();
  expect(shown()).toContain('Waiting for you');
  expect(shown()).toContain('Ojay store');
  expect(shown()).not.toContain('Acme');
  expect(forgetPayCheckouts).toHaveBeenCalledWith([OTHER]);
  await act(async () => { tree.root.findAll((n) => typeof n.props.testID === 'string' && n.props.testID.startsWith('Pay Ojay store'))[0].props.onPress(); });
  expect(router.push).toHaveBeenCalledWith(`/pay?id=${ID}`);
});

it('a remembered payment the server can\'t read right now keeps its row; a gone one is forgotten', async () => {
  jest.mocked(listPayCheckouts).mockResolvedValue([remembered(ID, 'Ojay store'), remembered(OTHER, 'Acme')]);
  jest.mocked(getCheckout).mockImplementation(async (id: string) => {
    throw Object.assign(new Error(id === ID ? 'offline' : 'payment not found'), { status: id === ID ? undefined : 404 });
  });
  await mount();
  expect(shown()).toContain('Ojay store');
  expect(shown()).not.toContain('Acme');
  // One write for every gone row; a row that can't be read now is kept.
  expect(forgetPayCheckouts).toHaveBeenCalledTimes(1);
  expect(forgetPayCheckouts).toHaveBeenCalledWith([OTHER]);
});

it('with nothing waiting, there is no Waiting for you section', async () => {
  jest.mocked(listPayCheckouts).mockResolvedValue([]);
  await mount();
  expect(shown()).not.toContain('Waiting for you');
});

it('Recent payments is the activity feed, payments only, with See all on the payments filter', async () => {
  await mount();
  // Read once on opening the screen, not twice (the first focus is the mount's own read).
  expect(tree.root.findAll((n) => (n as any).type === 'mock-feed')[0].props.types).toEqual(['payment']);
  const feed = tree.root.findAll((n) => (n as any).type === 'mock-feed')[0];
  expect(feed.props.types).toEqual(['payment']);
  expect(feed.props.embedded).toBe(true);
  act(() => feed.props.onSeeAll());
  expect(router.push).toHaveBeenCalledWith('/wallet/history?types=payment');
});

it('Get paid shows only for a TSA Pay merchant, and a suspended one may not request', async () => {
  await mount();
  expect(shown()).not.toContain('Get paid');
  act(() => tree.unmount());

  jest.mocked(useOwnPayMerchant).mockReturnValue({ load: { kind: 'ready', merchant: { status: 'suspended' } as any }, reload: jest.fn() });
  await mount();
  expect(shown()).toContain('Get paid');
  expect(tree.root.findAll((n) => (n as any).type === 'mock-pay-link-entry')[0].props.canRequest).toBe(false);
});
