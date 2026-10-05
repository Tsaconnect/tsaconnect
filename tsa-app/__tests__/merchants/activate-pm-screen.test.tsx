/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The activation screen under both inbound models. With virtual accounts the
 * merchant must give a BVN/NIN to mint the account buyers pay into. Under
 * central inbound buyers pay the company account, nothing is minted, and the
 * screen must neither ask for an id nor send one.
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
    fonts: new Proxy({}, { get: () => 'Inter' }),
    type: new Proxy({}, { get: () => ({ fontSize: 14 }) }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress, disabled: props.disabled }, props.title),
    Chip: (props: any) => react.createElement(RNText, { accessibilityLabel: `chip:${props.label}` }, props.label),
    Icon: () => null,
    Spinner: () => null,
  };
});
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn() },
  Stack: { Screen: () => null },
}));
const mockStatus = { isPm: false, status: 'approved', activationNeedsId: true, loading: false, reload: jest.fn() };
jest.mock('@/hooks/useIsPaymentMerchant', () => ({
  __esModule: true,
  useIsPaymentMerchant: () => mockStatus,
}));
jest.mock('@/utils/kycRefusal', () => ({
  __esModule: true,
  promptKycRefusal: jest.fn((code?: string) => code === 'kyc_verification_required'),
}));
jest.mock('@/services/p2p', () => ({
  __esModule: true,
  activatePmAccount: jest.fn().mockResolvedValue({ success: true }),
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { activatePmAccount } from '@/services/p2p';
import { promptKycRefusal } from '@/utils/kycRefusal';
import ActivatePmScreen from '@/app/merchants/activate-pm';

let tree: ReactTestRenderer;
/** The BVN/NIN field — the only number-pad input on the screen. */
const idInputs = () => tree.root.findAllByProps({ keyboardType: 'number-pad' });
async function render() {
  await act(async () => {
    tree = TestRenderer.create(<ActivatePmScreen />);
  });
}

beforeEach(() => {
  jest.clearAllMocks();
  jest.spyOn(Alert, 'alert').mockImplementation(() => {});
});

describe('ActivatePmScreen', () => {
  it('asks for a BVN/NIN when activation mints a virtual account', async () => {
    mockStatus.activationNeedsId = true;
    await render();
    expect(idInputs().length).toBeGreaterThan(0);
    expect(tree.root.findAll((n) => n.props.accessibilityLabel === 'chip:BVN').length).toBeGreaterThan(0);
  });

  it('asks for no id under central inbound and activates without one', async () => {
    mockStatus.activationNeedsId = false;
    await render();
    expect(idInputs()).toHaveLength(0);
    expect(tree.root.findAll((n) => n.props.accessibilityLabel === 'chip:BVN')).toHaveLength(0);

    const [button] = tree.root.findAll((n) => n.props.accessibilityLabel === 'button:Activate');
    expect(button.props.disabled).toBeFalsy();
    await act(async () => {
      await button.props.onPress();
    });
    expect(activatePmAccount).toHaveBeenCalledWith();
  });

  it('sends an unverified merchant to verification instead of a generic failure', async () => {
    mockStatus.activationNeedsId = false;
    (activatePmAccount as jest.Mock).mockResolvedValueOnce({
      success: false,
      message: 'Verify your identity before activating as a payment merchant',
      code: 'kyc_verification_required',
    });
    await render();
    const [button] = tree.root.findAll((n) => n.props.accessibilityLabel === 'button:Activate');
    await act(async () => {
      await button.props.onPress();
    });
    expect(promptKycRefusal).toHaveBeenCalledWith(
      'kyc_verification_required',
      'Verify your identity before activating as a payment merchant'
    );
    expect(Alert.alert).not.toHaveBeenCalledWith('Activation failed', expect.anything());
  });
});
