/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * The Didit session WebView holds the camera, so it only ever shows Didit
 * (L6): https pages on Didit's hosts load, anything else never does; the
 * camera is granted silently only to the page's own host; and the end of the
 * flow — the callback deep link, first-time KYC's or a name change's — is
 * caught in the WebView and reported once, never handed to the OS.
 */
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});
jest.mock('@/theme', () => ({ __esModule: true, useTheme: () => ({ colors: new Proxy({}, { get: () => '#000' }) }) }));
jest.mock('expo-camera', () => ({ __esModule: true, Camera: { requestCameraPermissionsAsync: jest.fn() } }));
const mockWebView: { props: any } = { props: null };
jest.mock('react-native-webview', () => ({
  __esModule: true,
  WebView: (props: any) => { mockWebView.props = props; return null; },
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act } from 'react-test-renderer';
import { DiditWebView, isDiditFinished, isDiditOrigin, shouldLoadInDidit } from '../DiditSession';

beforeEach(() => { mockWebView.props = null; });

it('only https pages on Didit\'s hosts are Didit', () => {
  for (const ok of ['https://verify.didit.me/session/abc', 'https://didit.me/', 'https://business.didit.me:443/x?y=1']) {
    expect(isDiditOrigin(ok)).toBe(true);
  }
  for (const bad of [
    'http://verify.didit.me/session/abc', 'https://evil.com/?u=https://verify.didit.me', 'https://didit.me.evil.com/',
    'https://notdidit.me/', 'https://evil.com#.didit.me', 'javascript:alert(1)', 'tsaconnect://kyc/callback',
  ]) {
    expect(isDiditOrigin(bad)).toBe(false);
  }
});

it('the end of the flow is caught — first-time KYC\'s callback and a name change\'s alike', () => {
  expect(isDiditFinished('tsaconnect://kyc/callback')).toBe(true);
  expect(isDiditFinished('tsaconnect://kyc/callback?flow=name_change&verificationSessionId=s1&status=Approved')).toBe(true);
  expect(isDiditFinished('https://verify.didit.me/session/abc')).toBe(false);
});

it('decides what the WebView may load: Didit yes, the finish never (reported), anything else no', () => {
  const finished = jest.fn();
  expect(shouldLoadInDidit('https://verify.didit.me/session/abc', finished)).toBe(true);
  expect(shouldLoadInDidit('about:blank', finished)).toBe(true);
  expect(shouldLoadInDidit('https://phish.example/login', finished)).toBe(false);
  expect(finished).not.toHaveBeenCalled();
  expect(shouldLoadInDidit('tsaconnect://kyc/callback?flow=name_change', finished)).toBe(false);
  expect(finished).toHaveBeenCalledTimes(1);
});

it('the WebView: grants the camera only to the page\'s own host, allowlists navigation, and finishes once', async () => {
  const onFinished = jest.fn();
  await act(async () => { TestRenderer.create(<DiditWebView url="https://verify.didit.me/session/abc" onFinished={onFinished} />); });
  const p = mockWebView.props;
  expect(p.source).toEqual({ uri: 'https://verify.didit.me/session/abc' });
  expect(p.mediaCapturePermissionGrantType).toBe('grantIfSameHostElsePrompt');
  expect(p.onPermissionRequest).toBeUndefined();

  const top = (url: string) => p.onShouldStartLoadWithRequest({ url, isTopFrame: true });
  expect(top('https://verify.didit.me/step/2')).toBe(true);
  expect(top('https://phish.example/')).toBe(false);
  expect(top('tsaconnect://kyc/callback?flow=name_change')).toBe(false);
  p.onNavigationStateChange({ url: 'tsaconnect://kyc/callback' });
  expect(onFinished).toHaveBeenCalledTimes(1);
});

it('a session URL that is not Didit\'s is never opened', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  const onFinished = jest.fn();
  await act(async () => { TestRenderer.create(<DiditWebView url="https://phish.example/session" onFinished={onFinished} />); });
  expect(mockWebView.props).toBeNull();
  expect(alert).toHaveBeenCalled();
  expect(onFinished).toHaveBeenCalledTimes(1);
  alert.mockRestore();
});
