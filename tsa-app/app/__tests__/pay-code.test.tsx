/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * TSA Pay by code: a payer types or pastes the code a business gave her and
 * lands on the ordinary checkout. The code is forgiven how she typed it; a
 * wrong one, or too many tries, says so in her words.
 */
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
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
    Input: (props: any) =>
      react.createElement(View, null,
        props.leftAdornment,
        react.createElement(TextInput, { testID: 'code', value: props.value, onChangeText: props.onChangeText }),
        props.rightAdornment,
        props.error ? react.createElement(RNText, null, props.error) : null),
  };
});
jest.mock('@/services/payApi', () => ({ __esModule: true, lookupPayCode: jest.fn() }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as Clipboard from 'expo-clipboard';
import { router } from 'expo-router';
import { lookupPayCode } from '@/services/payApi';
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

it('Paste fills the field from the clipboard', async () => {
  jest.mocked(Clipboard.getStringAsync).mockResolvedValue('TSA-7K2QF-M9XWD\n');
  await mount();
  await press('Paste');
  expect(host('code').props.value).toBe('7K2QF-M9XWD');
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
