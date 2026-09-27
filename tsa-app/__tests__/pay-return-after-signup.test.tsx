/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A new customer who tapped a pay link signs up, verifies her email and may
 * have to pass KYC before she can pay. What this pins: finishing signup (the
 * email code, or skipping it — only on the signup path, never from Profile)
 * and finishing KYC hand off through goAfterAuth —
 * the one post-auth mechanism — so a remembered payment brings her back to it;
 * KYC offers "Continue to payment" only when one is waiting, and goes back to
 * the pay screen underneath when it was opened from there.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
const mockParams: { returnTo?: string; from?: string; email?: string } = {};
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn(), push: jest.fn(), back: jest.fn(), canGoBack: jest.fn(() => true) },
  useLocalSearchParams: () => mockParams,
}));
jest.mock('@react-navigation/native', () => {
  const react = jest.requireActual('react');
  return { __esModule: true, useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]) };
});
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    AccentCard: box,
    Icon: () => null,
    IconButton: () => null,
    PressableScale: (props: any) => react.createElement(View, { accessibilityLabel: props.accessibilityLabel, onPress: props.onPress }, props.children),
    Spinner: () => null,
    ScreenHeader: () => null,
  };
});
jest.mock('react-native-otp-entry', () => {
  const react = jest.requireActual('react');
  const { TextInput } = jest.requireActual('react-native');
  return {
    __esModule: true,
    OtpInput: (props: any) => react.createElement(TextInput, { accessibilityLabel: 'otp', onChangeText: props.onFilled }),
  };
});
jest.mock('react-native-webview', () => ({ __esModule: true, WebView: () => null }));
jest.mock('expo-camera', () => ({ __esModule: true, Camera: { requestCameraPermissionsAsync: jest.fn() } }));
jest.mock('@/utils/back', () => ({ __esModule: true, goBack: jest.fn() }));
jest.mock('../AuthContext/AuthContext', () => ({ __esModule: true, useAuth: () => ({ setEmailVerified: jest.fn() }) }));
jest.mock('../components/services/api', () => ({
  __esModule: true,
  default: {
    verifyOtp: jest.fn(async () => ({ success: true })),
    resendOtp: jest.fn(),
    getKYCStatus: jest.fn(async () => ({ success: true, data: { verificationStatus: 'verified' } })),
    createKYCSession: jest.fn(),
    getErrorMessage: () => '',
  },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { router } from 'expo-router';
import VerifyScreen from '@/app/verify';
import KYCScreen from '@/app/profile/kyc';
import { rememberPendingPayment } from '@/utils/postAuthRoute';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const inADay = () => new Date(Date.now() + 24 * 60 * 60_000).toISOString();

let tree: ReactTestRenderer;
const flush = () => act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
const byLabel = (label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};
const text = () => JSON.stringify(tree.toJSON());
async function mount(el: React.ReactElement) {
  await act(async () => { tree = TestRenderer.create(el); });
  await flush();
}

beforeEach(async () => {
  jest.clearAllMocks();
  await AsyncStorage.clear();
  delete mockParams.returnTo;
  delete mockParams.from;
});
afterEach(() => { act(() => tree?.unmount()); });

describe('finishing signup', () => {
  beforeEach(() => { mockParams.from = 'signup'; });

  it('verifying the email returns her to the payment she came from', async () => {
    await rememberPendingPayment(ID, { expiresAt: inADay() });
    await mount(<VerifyScreen />);
    await act(async () => { byLabel('otp').props.onChangeText('123456'); });
    await act(async () => { byLabel('button:Verify').props.onPress(); });
    await flush();
    expect(router.replace).toHaveBeenCalledWith(`/pay?id=${ID}`);
  });

  it('skipping the email code does too; with nothing waiting, both land home', async () => {
    await rememberPendingPayment(ID, { expiresAt: inADay() });
    await mount(<VerifyScreen />);
    await act(async () => { byLabel('Skip verification for now').props.onPress(); });
    await flush();
    expect(router.replace).toHaveBeenCalledWith(`/pay?id=${ID}`);

    jest.mocked(router.replace).mockClear();
    await act(async () => { byLabel('Skip verification for now').props.onPress(); });
    await flush();
    expect(router.replace).toHaveBeenCalledWith('/home');
  });
});

describe('verifying the email from Profile', () => {
  it('never jumps to a remembered payment', async () => {
    await rememberPendingPayment(ID, { expiresAt: inADay() });
    await mount(<VerifyScreen />);
    await act(async () => { byLabel('otp').props.onChangeText('123456'); });
    await act(async () => { byLabel('button:Verify').props.onPress(); });
    await flush();
    expect(router.replace).toHaveBeenCalledWith('/home');
    expect(router.replace).not.toHaveBeenCalledWith(`/pay?id=${ID}`);
  });
});

describe('finishing KYC', () => {
  it('offers the way back to a waiting payment, opening it afresh when nothing is underneath', async () => {
    await rememberPendingPayment(ID, { expiresAt: inADay() });
    await mount(<KYCScreen />);
    expect(text()).toContain('Identity Verified');
    await act(async () => { byLabel('button:Continue to payment').props.onPress(); });
    await flush();
    expect(router.replace).toHaveBeenCalledWith(`/pay?id=${ID}`);
  });

  it('opened from the pay screen, goes back to it', async () => {
    mockParams.returnTo = 'pay';
    await rememberPendingPayment(ID, { expiresAt: inADay() });
    await mount(<KYCScreen />);
    await act(async () => { byLabel('button:Continue to payment').props.onPress(); });
    expect(router.back).toHaveBeenCalled();
    expect(router.replace).not.toHaveBeenCalled();
  });

  it('with no payment waiting, it is just "Go Back"', async () => {
    await mount(<KYCScreen />);
    expect(text()).toContain('Identity Verified');
    expect(() => byLabel('button:Continue to payment')).toThrow();
  });
});
