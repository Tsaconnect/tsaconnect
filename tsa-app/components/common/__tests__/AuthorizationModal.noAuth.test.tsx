/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * With neither a PIN nor biometrics — a customer who just signed up to pay a
 * link — the authorization sheet offers to set up a PIN instead of a dead end.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000000' }), spacing: new Proxy({}, { get: () => 8 }), radius: new Proxy({}, { get: () => 8 }) }),
}));
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
jest.mock('../../../services/localAuth', () => ({
  __esModule: true,
  authenticateWithBiometric: jest.fn(),
  getBiometricType: jest.fn(async () => 'Biometrics'),
  hasPin: jest.fn(async () => false),
  isBiometricAvailable: jest.fn(async () => false),
  verifyPin: jest.fn(),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    AccentCard: box, Card: box, Icon: () => null, Input: () => null, PressableScale: box, Spinner: () => null,
    Button: (props: any) => react.createElement(RNText, null, props.title),
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Sheet: (props: any) => (props.visible ? react.createElement(View, null, props.children) : null),
    EmptyState: (props: any) => react.createElement(RNText, { accessibilityLabel: 'empty', onPress: props.onAction }, `${props.title} ${props.actionLabel}`),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import AuthorizationModal from '../AuthorizationModal';

it('offers to set up a PIN, closing itself first', async () => {
  const onCancel = jest.fn();
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = TestRenderer.create(<AuthorizationModal visible title="Authorize" onAuthorized={jest.fn()} onCancel={onCancel} />);
  });
  const empty = tree.root.findAll((n) => n.props.accessibilityLabel === 'empty')[0];
  expect(JSON.stringify(tree.toJSON())).toContain('Set up a PIN first Set up PIN');
  await act(async () => { empty.props.onPress(); });
  expect(onCancel).toHaveBeenCalled();
  expect(router.push).toHaveBeenCalledWith('/pin-setup');
});
