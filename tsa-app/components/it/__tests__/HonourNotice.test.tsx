/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The rate hold on a routed stablecoin buy: a countdown while her quote is
 * held, what happens once it ends, and "Rate updated" after a late transfer.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000' }), spacing: { xs: 4, sm: 8, md: 12 } }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return { __esModule: true, Text: (props: any) => react.createElement(Text, null, props.children) };
});

jest.mock('@/hooks/useCountdown', () => ({ __esModule: true, useCountdown: jest.fn() }));

import React from 'react';
import { Text } from 'react-native';
import { useCountdown } from '@/hooks/useCountdown';
import { HonourNotice, type HonourNoticeProps } from '../HonourNotice';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');
const countdown = jest.mocked(useCountdown);

function texts(props: HonourNoticeProps): string {
  let tree: any;
  TestRenderer.act(() => {
    tree = TestRenderer.create(<HonourNotice {...props} />);
  });
  return tree.root.findAllByType(Text).map((n: any) => String(n.props.children)).join(' | ');
}

const held = { honourUntil: '2026-10-03T12:15:00Z', pending: true, rateUpdated: false, receive: '96 USDC' };

describe('HonourNotice', () => {
  it('counts down while the rate is held', () => {
    countdown.mockReturnValue({ label: '12:34', expired: false, secs: 754 });
    const out = texts(held);
    expect(out).toContain('Rate held for');
    expect(out).toContain('12:34');
    expect(out).toContain('exactly 96 USDC');
  });

  it('says what happens once the hold ends', () => {
    countdown.mockReturnValue({ label: '0:00', expired: true, secs: 0 });
    const out = texts(held);
    expect(out).toContain('Your rate hold has ended');
    expect(out).not.toContain('Rate held for');
  });

  it('shows Rate updated after a late transfer', () => {
    countdown.mockReturnValue({ label: '0:00', expired: true, secs: 0 });
    expect(texts({ ...held, pending: false, rateUpdated: true, receive: '88.76 USDC' }))
      .toContain("Rate updated: you'll receive 88.76 USDC");
  });

  it('renders nothing for a buy with no hold', () => {
    countdown.mockReturnValue({ label: '0:00', expired: true, secs: 0 });
    expect(texts({ pending: true, rateUpdated: false, receive: '1 USDC' })).toBe('');
  });
});
