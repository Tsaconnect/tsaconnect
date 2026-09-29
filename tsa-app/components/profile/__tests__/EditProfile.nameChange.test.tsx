/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * A verified user's name is the one on her ID: the profile form shows it
 * read-only with the way to change it (only while a change can be started —
 * useNameChangeEntry), and the server's use_name_change refusal sends her
 * there. An unverified user still edits her name here.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
    fonts: new Proxy({}, { get: () => 'Inter' }),
    type: new Proxy({}, { get: () => ({ fontSize: 14 }) }),
  }),
}));
const mockPush = jest.fn();
jest.mock('expo-router', () => ({ __esModule: true, useRouter: () => ({ push: mockPush }) }));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText, TextInput } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Icon: () => null,
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    Input: (props: any) => react.createElement(TextInput, {
      accessibilityLabel: `input:${props.label}`, value: props.value, onChangeText: props.onChangeText, editable: props.editable,
    }),
  };
});
jest.mock('react-native-international-phone-number', () => ({ __esModule: true, default: () => null }));
jest.mock('expo-image-picker', () => ({ __esModule: true }));
jest.mock('@/hooks/useKeyboardHeight', () => ({ __esModule: true, useKeyboardHeight: () => 0 }));
jest.mock('../../../AuthContext/AuthContext', () => ({
  __esModule: true,
  useAuth: () => ({ token: 'Bearer jwt', setCurrentUser: jest.fn() }),
}));
const mockEntry = jest.fn((verified: boolean) => verified);
jest.mock('@/hooks/useNameChangeEntry', () => ({ __esModule: true, useNameChangeEntry: (v: boolean) => mockEntry(v) }));
jest.mock('../../services/api', () => ({
  __esModule: true,
  default: { setToken: jest.fn(), updateProfile: jest.fn(), uploadImage: jest.fn() },
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import apiService from '../../services/api';
import EditProfileScreen from '../EditProfile';

const byLabel = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};

async function mount(user: Record<string, string>) {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<EditProfileScreen user={user} />); });
  return tree;
}

beforeEach(() => {
  jest.clearAllMocks();
  mockEntry.mockImplementation((verified: boolean) => verified);
});

it('a verified name is read-only, with the way to change it', async () => {
  const tree = await mount({ name: 'Ada Obi', verificationStatus: 'verified' });
  expect(byLabel(tree, 'input:Full Name').props.editable).toBe(false);
  await act(async () => { byLabel(tree, 'button:Change name').props.onPress(); });
  expect(mockPush).toHaveBeenCalledWith('/profile/name-change');
});

it('with name changes switched off, the verified name stays read-only with no way offered', async () => {
  mockEntry.mockReturnValue(false);
  const tree = await mount({ name: 'Ada Obi', verificationStatus: 'verified' });
  expect(byLabel(tree, 'input:Full Name').props.editable).toBe(false);
  expect(() => byLabel(tree, 'button:Change name')).toThrow();
});

it('an unverified user edits her name here', async () => {
  const tree = await mount({ name: 'Ada Obi', verificationStatus: 'unverified' });
  expect(byLabel(tree, 'input:Full Name').props.editable).toBe(true);
  expect(() => byLabel(tree, 'button:Change name')).toThrow();
});

it('the server\'s use_name_change refusal offers the name-change flow, in its own words', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  jest.mocked(apiService.updateProfile).mockResolvedValue({
    success: false, code: 'use_name_change', message: 'Your name comes from your verified ID — change it by verifying again.',
  });
  const tree = await mount({ name: 'Ada Obi', verificationStatus: 'unverified' });
  await act(async () => { byLabel(tree, 'button:Save Changes').props.onPress(); });
  const [title, message, buttons] = alert.mock.calls[0] as [string, string, any[]];
  expect(title).toBe('Change your name');
  expect(message).toBe('Your name comes from your verified ID — change it by verifying again.');
  buttons.find((b) => b.text === 'Change name').onPress();
  expect(mockPush).toHaveBeenCalledWith('/profile/name-change');
  alert.mockRestore();
});
