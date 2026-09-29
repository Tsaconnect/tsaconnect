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
import { Alert, Linking } from 'react-native';
import TestRenderer, { act } from 'react-test-renderer';
import {
  DIDIT_ORIGIN_WHITELIST, DiditWebView, isDiditFinished, isDiditOrigin, shouldLoadInDidit, shouldLoadInDiditFrame,
} from '../DiditSession';

beforeEach(() => { mockWebView.props = null; });

it('only https pages on Didit\'s hosts are Didit', () => {
  for (const ok of ['https://verify.didit.me/session/abc', 'https://didit.me/', 'https://business.didit.me:443/x?y=1']) {
    expect(isDiditOrigin(ok)).toBe(true);
  }
  for (const bad of [
    'http://verify.didit.me/session/abc', 'https://evil.com/?u=https://verify.didit.me', 'https://didit.me.evil.com/',
    'https://notdidit.me/', 'https://evil.com#.didit.me', 'javascript:alert(1)', 'tsaconnect://kyc/callback',
    // A browser reads a backslash as a slash: this is evil.com.
    'https://evil.com\\@verify.didit.me/', 'https:\\\\verify.didit.me/', 'https://verify.didit.me\\.evil.com/',
    // User info, and the tricks a regex-only URL parser (React Native's) falls for.
    'https://verify.didit.me@evil.com/', 'https://evil.com@verify.didit.me/', 'https://evil.com#@verify.didit.me',
    'https://evil.com?@verify.didit.me', 'https://evil.com/@verify.didit.me', ' https://verify.didit.me/', 'https://verify.didit.me /x',
    'not a url',
  ]) {
    expect(isDiditOrigin(bad)).toBe(false);
  }
});

it('holds under React Native\'s own URL, which does not validate what it parses', () => {
  const { URL: RNURL } = jest.requireActual('react-native/Libraries/Blob/URL');
  // What makes the guard necessary: RN reads this as Didit's host.
  expect(new RNURL('https://evil.com#@verify.didit.me').hostname).toBe('verify.didit.me');
  const whatwg = global.URL;
  (global as any).URL = RNURL;
  try {
    expect(isDiditOrigin('https://verify.didit.me/session/abc')).toBe(true);
    for (const bad of ['https://evil.com#@verify.didit.me', 'https://evil.com?@verify.didit.me', 'https://evil.com/@verify.didit.me',
      'https://evil.com\\@verify.didit.me/', 'https://verify.didit.me@evil.com/']) {
      expect(isDiditOrigin(bad)).toBe(false);
    }
  } finally {
    (global as any).URL = whatwg;
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

it('a frame inside the page is held to the same allowlist, and never finishes the flow', () => {
  for (const ok of ['https://verify.didit.me/frame', 'about:blank', 'about:srcdoc']) expect(shouldLoadInDiditFrame(ok)).toBe(true);
  for (const bad of ['https://phish.example/', 'https://evil.com\\@verify.didit.me/', 'tsaconnect://kyc/callback', 'data:text/html,x']) {
    expect(shouldLoadInDiditFrame(bad)).toBe(false);
  }
});

it('the WebView: grants the camera only to the page\'s own host, allowlists navigation, and finishes once', async () => {
  const onFinished = jest.fn();
  await act(async () => { TestRenderer.create(<DiditWebView url="https://verify.didit.me/session/abc" onFinished={onFinished} />); });
  const p = mockWebView.props;
  expect(p.source).toEqual({ uri: 'https://verify.didit.me/session/abc' });
  expect(p.originWhitelist).toEqual(DIDIT_ORIGIN_WHITELIST);
  expect(p.mediaCapturePermissionGrantType).toBe('grantIfSameHostElsePrompt');
  expect(p.onPermissionRequest).toBeUndefined();

  const top = (url: string) => p.onShouldStartLoadWithRequest({ url, isTopFrame: true });
  expect(top('https://verify.didit.me/step/2')).toBe(true);
  expect(top('https://phish.example/')).toBe(false);
  const frame = (url: string) => p.onShouldStartLoadWithRequest({ url, isTopFrame: false });
  expect(frame('https://verify.didit.me/liveness')).toBe(true);
  expect(frame('https://phish.example/')).toBe(false);
  expect(frame('tsaconnect://kyc/callback')).toBe(false);
  expect(onFinished).not.toHaveBeenCalled();
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

it('in react-native-webview\'s real order — the origin list, then the handler — every URL the handler decides reaches it', async () => {
  // The library's own gate: a URL off the origin list is handed to the OS and
  // never loaded, without the handler hearing of it.
  const { createOnShouldStartLoadWithRequest } = jest.requireActual('react-native-webview/lib/WebViewShared');
  const canOpen = jest.spyOn(Linking, 'canOpenURL').mockResolvedValue(false);
  const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
  const onFinished = jest.fn();
  await act(async () => { TestRenderer.create(<DiditWebView url="https://verify.didit.me/session/abc" onFinished={onFinished} />); });
  const p = mockWebView.props;
  const decided: Record<string, boolean> = {};
  const gate = createOnShouldStartLoadWithRequest(
    (ok: boolean, url: string) => { decided[url] = ok; }, p.originWhitelist, p.onShouldStartLoadWithRequest);
  const load = (url: string, isTopFrame: boolean) => { gate({ nativeEvent: { url, isTopFrame, lockIdentifier: 1 } }); return decided[url]; };

  // A frame's srcdoc document reaches the handler (it was dropped by the list
  // before 'about:*' was on it) and is allowed; it is not a top page.
  expect(load('about:srcdoc', false)).toBe(true);
  expect(load('about:blank', true)).toBe(true);
  expect(load('about:srcdoc', true)).toBe(false);
  expect(load('https://verify.didit.me/step/2', true)).toBe(true);
  expect(load('https://phish.example/', false)).toBe(false);
  expect(canOpen).not.toHaveBeenCalled();
  // Off the list: never loaded, never reaching the handler.
  expect(load('data:text/html,x', false)).toBe(false);
  expect(canOpen).toHaveBeenCalledWith('data:text/html,x');
  // The callback is on the list so the handler can catch it: not loaded, reported once.
  expect(load('tsaconnect://kyc/callback?flow=name_change', true)).toBe(false);
  expect(onFinished).toHaveBeenCalledTimes(1);
  canOpen.mockRestore();
  warn.mockRestore();
});
