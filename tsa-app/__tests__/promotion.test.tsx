/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText, Pressable } = jest.requireActual('react-native');
  return {
    Text: (p: any) => react.createElement(RNText, null, p.children),
    Badge: (p: any) => react.createElement(RNText, null, p.label),
    Card: (p: any) => react.createElement(View, null, p.children),
    Button: (p: any) =>
      react.createElement(Pressable, { onPress: p.onPress, accessibilityLabel: p.title },
        react.createElement(RNText, null, p.title)),
  };
});
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return { useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]) };
});
jest.mock('@/AuthContext/AuthContext', () => ({
  useAuth: () => ({ currentUser: { _id: 'buyer', referralCode: 'ABC' } }),
}));
const mockClaim = jest.fn();
const mockStatus = jest.fn();
jest.mock('@/components/services/api', () => ({
  __esModule: true,
  default: {
    claimPromo: (...a: any[]) => mockClaim(...a),
    getPromoStatus: (...a: any[]) => mockStatus(...a),
    getProfile: jest.fn(),
  },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { PromoPrice } from '@/components/promotion/PromoPrice';
import { PromoUnlockCard } from '@/components/promotion/PromoUnlockCard';

const fmt = (n: number) => `$${n}`;
const promoted = {
  id: 'p1', userId: 'seller', price: 100, promoEnabled: true, promoPrice: 10,
  promoDiscountPercent: 90, promoRequiredReferrals: 5, promoCountdownSeconds: 600,
};
const texts = (r: ReactTestRenderer) => JSON.stringify(r.toJSON());
const render = (el: React.ReactElement) => {
  let r!: ReactTestRenderer;
  act(() => { r = TestRenderer.create(el); });
  return r;
};
const flush = () => act(async () => { await Promise.resolve(); });

describe('PromoPrice', () => {
  it('shows tag, promo price, struck original and percent off', () => {
    const r = render(<PromoPrice product={promoted} format={fmt} />);
    const t = texts(r);
    expect(t).toContain('Promotion');
    expect(t).toContain('$10');
    expect(t).toContain('$100');
    expect(t).toContain('(90% off)');
  });
  it('renders the plain price when not promoted or forced to full price', () => {
    const plain = render(<PromoPrice product={{ price: 100 }} format={fmt} />);
    expect(texts(plain)).toContain('$100');
    expect(texts(plain)).not.toContain('off)');
    const full = render(<PromoPrice product={promoted} format={fmt} fullPrice />);
    expect(texts(full)).not.toContain('$10"');
    expect(texts(full)).not.toContain('Promotion');
  });
});

describe('PromoUnlockCard', () => {
  beforeEach(() => {
    jest.useFakeTimers();
    jest.clearAllMocks();
    mockStatus.mockResolvedValue({ success: true, data: { required: 5, current: 0, claimed: false, expired: false, unlocked: false } });
  });
  afterEach(() => jest.useRealTimers());

  const mount = async (product = promoted) => {
    let r!: ReactTestRenderer;
    await act(async () => { r = TestRenderer.create(<PromoUnlockCard product={product} format={fmt} />); });
    await flush();
    return r;
  };

  it('offers to unlock, then shows progress and countdown after claiming', async () => {
    const r = await mount();
    expect(texts(r)).toContain('bring 5 verified referrals to unlock 90% off');
    mockClaim.mockResolvedValue({ success: true, data: {
      required: 5, current: 3, claimed: true, expired: false, unlocked: false,
      expiresAt: new Date(Date.now() + 125000).toISOString() } });
    await act(async () => { r.root.findAllByProps({ accessibilityLabel: 'Unlock discount' })[0].props.onPress(); });
    expect(mockClaim).toHaveBeenCalledWith('p1');
    expect(texts(r)).toContain('3 / 5 verified referrals');
    expect(texts(r)).toContain('Time left 02:05');
    await act(async () => { jest.advanceTimersByTime(2000); });
    expect(texts(r)).toContain('Time left 02:03');
    expect(texts(r)).toContain('Share my referral link');
    r.unmount();
  });

  it('shows unlocked and expired states', async () => {
    mockStatus.mockResolvedValue({ success: true, data: { required: 5, current: 5, claimed: true, expired: false, unlocked: true } });
    const unlocked = await mount();
    expect(texts(unlocked)).toContain('Discount unlocked - you pay $10');
    mockStatus.mockResolvedValue({ success: true, data: { required: 5, current: 1, claimed: true, expired: true, unlocked: false } });
    const expired = await mount();
    expect(texts(expired)).toContain("Time's up - this item is now full price");
    expect(texts(expired)).toContain('$100');
  });

  it('is hidden for the owner and for non-promoted listings', async () => {
    expect((await mount({ ...promoted, userId: 'buyer' })).toJSON()).toBeNull();
    expect((await mount({ ...promoted, promoEnabled: false })).toJSON()).toBeNull();
    expect(mockStatus).not.toHaveBeenCalled();
  });
});
