/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Finding and applying for TSA Pay in the app. What this pins: the Profile row
 * and the Merchant dashboard card both open the TSA Pay application; an
 * unverified applicant is sent to verification instead of the form (and so is
 * one the server refuses with kyc_verification_required); an approved merchant is pointed at
 * the web app to set up, and keeps Payment Settings.
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
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    PressableScale: (props: any) => react.createElement(View, { accessibilityLabel: 'pressable', onPress: props.onPress }, props.children),
    Chip: (props: any) => react.createElement(RNText, null, props.label),
    Spinner: () => null,
    Icon: () => null,
    IconButton: () => null,
    Avatar: () => null,
    Divider: () => null,
    Badge: box,
    Card: box,
    AccentCard: box,
    SelectRow: () => null,
    PickerSheet: () => null,
  };
});
const mockSearch: { type?: string } = {};
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
    Stack: { Screen: () => null },
    useLocalSearchParams: () => mockSearch,
    // Focus once, on mount — what a first visit to the screen does.
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
jest.mock('@react-navigation/native', () => {
  const react = jest.requireActual('react');
  return { __esModule: true, useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]) };
});
jest.mock('expo-clipboard', () => ({ __esModule: true, setStringAsync: jest.fn() }));
jest.mock('../../components/services/api', () => ({
  __esModule: true,
  default: {
    getKYCStatus: jest.fn(),
    getStoredToken: jest.fn(async () => 'token'),
    getProfile: jest.fn(async () => ({ success: true, data: { name: 'Ada', email: 'ada@x.ng', verificationStatus: 'verified' } })),
    getErrorMessage: () => '',
    setToken: jest.fn(),
    getMerchantProducts: jest.fn(async () => ({ success: true, data: [] })),
  },
  getMyMerchantRequest: jest.fn(),
  submitMerchantRequest: jest.fn(),
}));
const mockAuth: { currentUser: any } = { currentUser: { verificationStatus: 'pending', phoneNumber: '+2348000000000' } };
jest.mock('../../AuthContext/AuthContext', () => ({ __esModule: true, useAuth: () => mockAuth }));
jest.mock('@/services/payMerchantApi', () => ({
  __esModule: true,
  ...jest.requireActual('@/services/payMerchantApi'),
  // Nobody here has TSA Pay yet: the dashboard shows the application card.
  getOwnMerchant: jest.fn(async () => { throw Object.assign(new Error('none'), { status: 404, code: 'no_merchant' }); }),
}));
jest.mock('../../services/ngBanks', () => ({ __esModule: true, getNigerianBanks: jest.fn(async () => []), resolveAccountName: jest.fn() }));
jest.mock('../../services/p2p', () => ({ __esModule: true, getMyPmStatus: jest.fn(async () => ({ isPm: false })) }));
jest.mock('@/hooks/useKeyboardHeight', () => ({ __esModule: true, useKeyboardHeight: () => 0 }));
jest.mock('@/components/common/LocationPicker', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) => react.createElement(Text, {
      accessibilityLabel: 'location',
      onPress: () => props.onChange({ country: 'Nigeria', state: 'Lagos', city: 'Ikeja' }),
    }, 'location'),
  };
});
jest.mock('@/contexts/CurrencyContext', () => ({ __esModule: true, useCurrency: () => ({ formatPrice: (n: number) => String(n) }) }));
const mockPmStatus = { isPm: false, status: null, loading: false, reload: () => {} };
jest.mock('@/hooks/useIsPaymentMerchant', () => ({ __esModule: true, useIsPaymentMerchant: () => mockPmStatus }));

import React from 'react';
import { Alert, Linking } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router as mockRouter } from 'expo-router';
import api, { getMyMerchantRequest, submitMerchantRequest } from '../../components/services/api';
import { TSA_PAY_APPLY } from '@/constants/routes';
import MerchantRequestScreen, { resolveMerchantGate } from '@/app/merchants/merchant-request';
import ProfileScreen from '@/components/profile/profile';
import MerchantDashboard from '@/app/merchants/dashboard/index';

const flush = () => act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
const text = (tree: ReactTestRenderer) => JSON.stringify(tree.toJSON());
const byLabel = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};
/** The innermost pressable around the text `label`. */
const pressableWith = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) =>
    n.props.accessibilityLabel === 'pressable' && n.findAll((c) => c.props.children === label).length > 0);
  if (!found.length) throw new Error(`no pressable reads ${label}`);
  return found[found.length - 1];
};

async function mount(el: React.ReactElement) {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(el); });
  await flush();
  return tree;
}

beforeEach(() => {
  jest.clearAllMocks();
  mockSearch.type = 'payment_acceptance';
  mockAuth.currentUser = { verificationStatus: 'pending', phoneNumber: '+2348000000000' };
  jest.mocked(getMyMerchantRequest).mockResolvedValue({ success: true, data: null });
  jest.mocked(api.getKYCStatus).mockResolvedValue({ success: true, data: { verificationStatus: 'pending' } } as any);
  jest.spyOn(Alert, 'alert').mockImplementation(() => {});
});

describe('entry points', () => {
  it('the TSA Pay application route is the form locked to payment_acceptance', () => {
    expect(TSA_PAY_APPLY).toBe('/merchants/merchant-request?type=payment_acceptance');
  });

  it('the Profile row opens the TSA Pay application', async () => {
    const tree = await mount(<ProfileScreen />);
    await act(async () => { pressableWith(tree, 'Accept payments with TSA Pay').props.onPress(); });
    expect(mockRouter.push).toHaveBeenCalledWith(TSA_PAY_APPLY);
  });

  it('the Merchant dashboard card opens the TSA Pay application', async () => {
    const tree = await mount(<MerchantDashboard />);
    await act(async () => { pressableWith(tree, 'Accept payments (TSA Pay)').props.onPress(); });
    expect(mockRouter.push).toHaveBeenCalledWith(TSA_PAY_APPLY);
  });
});

describe('verify first', () => {
  it('the gate swaps only the TSA Pay form for verification', () => {
    const none = { existingRequest: null, pmActive: false };
    expect(resolveMerchantGate({ ...none, formType: 'payment_acceptance', kycVerified: false })).toEqual({ screen: 'verify' });
    expect(resolveMerchantGate({ ...none, formType: 'payment_acceptance', kycVerified: true })).toEqual({ screen: 'form' });
    expect(resolveMerchantGate({ ...none, formType: 'general_products', kycVerified: false })).toEqual({ screen: 'form' });
    // A request she already has keeps its own status screen.
    expect(resolveMerchantGate({
      existingRequest: { status: 'pending', businessType: 'payment_acceptance' },
      formType: 'payment_acceptance', pmActive: false, kycVerified: false,
    })).toEqual({ screen: 'pending', pm: false });
  });

  it('an unverified applicant sees the verify card, not the form, and it opens the KYC screen', async () => {
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).toContain('Verify your identity first');
    expect(text(tree)).not.toContain('Submit Application');
    await act(async () => { byLabel(tree, 'button:Verify identity').props.onPress(); });
    expect(mockRouter.push).toHaveBeenCalledWith('/profile/kyc');
  });

  it('a fresh verified status shows the form even when the cached user is stale', async () => {
    jest.mocked(api.getKYCStatus).mockResolvedValue({ success: true, data: { verificationStatus: 'verified' } } as any);
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).toContain('Submit Application');
    expect(text(tree)).not.toContain('Verify your identity first');
  });

  it('nothing is painted until the KYC read settles — no flash of the verify card or the form', async () => {
    let settle!: (v: unknown) => void;
    jest.mocked(api.getKYCStatus).mockReturnValue(new Promise((r) => { settle = r; }) as any);
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).not.toContain('Verify your identity first');
    expect(text(tree)).not.toContain('Submit Application');
    await act(async () => { settle({ success: true, data: { verificationStatus: 'verified' } }); });
    await flush();
    expect(text(tree)).toContain('Submit Application');
  });

  it('a failed KYC read trusts a cached "verified" and shows the form', async () => {
    mockAuth.currentUser = { verificationStatus: 'verified', phoneNumber: '+2348000000000' };
    jest.mocked(api.getKYCStatus).mockRejectedValue(new Error('offline'));
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).toContain('Submit Application');
    expect(text(tree)).not.toContain('Verify your identity first');
  });

  it('a failed KYC read with no verified cache still asks her to verify', async () => {
    jest.mocked(api.getKYCStatus).mockRejectedValue(new Error('offline'));
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).toContain('Verify your identity first');
  });

  it('a kyc_verification_required refusal on submit shows the verify card', async () => {
    mockAuth.currentUser = { verificationStatus: 'verified', phoneNumber: '+2348000000000' };
    jest.mocked(api.getKYCStatus).mockResolvedValue({ success: false } as any);
    jest.mocked(submitMerchantRequest).mockResolvedValue({ success: false, code: 'kyc_verification_required', message: 'Verify your identity before applying for TSA Pay' });
    const tree = await mount(<MerchantRequestScreen />);
    const inputs = tree.root.findAll((n) => typeof n.props.placeholder === 'string' && n.props.onChangeText);
    const input = (placeholder: string) => inputs.find((n) => n.props.placeholder === placeholder)!;
    await act(async () => {
      input('e.g. Business Name').props.onChangeText('Sky Air');
      input('Street address').props.onChangeText('1 Airport Rd');
      input('https://example.com').props.onChangeText('https://skyair.ng');
      byLabel(tree, 'location').props.onPress();
    });
    await act(async () => { byLabel(tree, 'button:Submit Application').props.onPress(); });
    await flush();
    expect(submitMerchantRequest).toHaveBeenCalled();
    expect(text(tree)).toContain('Verify your identity first');
  });
});

describe('website', () => {
  /** A verified applicant's form, filled in except for the website. */
  async function filledForm() {
    mockAuth.currentUser = { verificationStatus: 'verified', phoneNumber: '+2348000000000' };
    jest.mocked(api.getKYCStatus).mockResolvedValue({ success: true, data: { verificationStatus: 'verified' } } as any);
    jest.mocked(submitMerchantRequest).mockResolvedValue({ success: true, data: { status: 'pending' } } as any);
    const tree = await mount(<MerchantRequestScreen />);
    const input = (placeholder: string) =>
      tree.root.findAll((n) => n.props.placeholder === placeholder && n.props.onChangeText)[0];
    await act(async () => {
      input('e.g. Business Name').props.onChangeText('Kemi Cakes');
      input('Street address').props.onChangeText('4 Allen Ave');
      byLabel(tree, 'location').props.onPress();
    });
    return { tree, website: (v: string) => act(async () => { input('https://example.com').props.onChangeText(v); }) };
  }
  const submit = (tree: ReactTestRenderer) =>
    act(async () => { byLabel(tree, 'button:Submit Application').props.onPress(); });

  it('is optional: a business with no website can apply', async () => {
    const { tree } = await filledForm();
    expect(text(tree)).toContain('Website (optional)');
    await submit(tree);
    await flush();
    expect(Alert.alert).not.toHaveBeenCalledWith('Error', expect.anything());
    expect(submitMerchantRequest).toHaveBeenCalledWith(expect.objectContaining({ businessType: 'payment_acceptance', website: '' }));
  });

  it('one that is given must still be an https address', async () => {
    const { tree, website } = await filledForm();
    await website('kemicakes.ng');
    await submit(tree);
    expect(Alert.alert).toHaveBeenCalledWith('Error', "Enter your website's https address");
    expect(submitMerchantRequest).not.toHaveBeenCalled();
  });
});

describe('approved', () => {
  it('points the merchant at the web app and keeps Payment Settings', async () => {
    mockAuth.currentUser = { verificationStatus: 'verified' };
    jest.mocked(getMyMerchantRequest).mockResolvedValue({
      success: true, data: { status: 'approved', businessType: 'payment_acceptance', businessName: 'Sky Air' },
    });
    const openURL = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    const tree = await mount(<MerchantRequestScreen />);
    expect(text(tree)).toContain('Set up on the web');
    expect(text(tree)).toContain('Create API keys, webhooks and payouts, and see your payments, at app.tsaconnectworld.com');
    await act(async () => { byLabel(tree, 'button:Open').props.onPress(); });
    expect(openURL).toHaveBeenCalledWith('https://app.tsaconnectworld.com');
    await act(async () => { byLabel(tree, 'button:Payment Settings').props.onPress(); });
    expect(mockRouter.push).toHaveBeenCalledWith('/merchants/pay-settings');
  });
});
