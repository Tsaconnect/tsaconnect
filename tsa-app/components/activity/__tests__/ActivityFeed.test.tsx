/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

// Tapping a feed row: an item with an in-app screen (Instant Transfer orders)
// opens it; anything else keeps opening its explorer link. A row with neither
// is not tappable, and a pre-resolved status shows its own label and tone.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('expo-router', () => ({ router: { push: jest.fn() } }));
jest.mock('@/hooks/useFiatSymbol', () => ({ useFiatSymbol: () => (c: string) => c }));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: (_t, key) => `color:${String(key)}` }),
    spacing: { xs: 4, sm: 8, md: 12, lg: 16 },
    radius: { pill: 999 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text, Pressable } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(Text, null, props.children),
    Badge: (props: any) =>
      react.createElement(Text, { testID: 'badge', style: { color: props.color } }, props.label),
    PressableScale: (props: any) =>
      react.createElement(Pressable, { ...props, testID: 'row' }, props.children),
    Icon: () => null,
    useToneColors: (tone: string) => ({ color: `tone:${tone}`, bg: `bg:${tone}` }),
  };
});

import React from 'react';
import { Linking } from 'react-native';
import { router } from 'expo-router';
import { ActivityRow, openActivityItem } from '../ActivityFeed';
import type { ActivityItem } from '@/services/activityService';

// The repo ships no @types/react-test-renderer, so it is loaded untyped rather
// than adding a TS7016 error to `tsc --noEmit`.
// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');

const base: ActivityItem = {
  id: 'x',
  type: 'wallet',
  title: 't',
  subtitle: 's',
  status: 'confirmed',
  timestamp: '2026-09-20T10:00:00Z',
  iconName: 'arrow-up',
  iconColor: '#000',
  iconBg: '#000',
};

function render(item: ActivityItem): any {
  let tree: any;
  TestRenderer.act(() => {
    tree = TestRenderer.create(<ActivityRow item={item} />);
  });
  return tree;
}

describe('openActivityItem', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
  });

  it('navigates in-app when the item has a detail screen', () => {
    const opened = openActivityItem({ ...base, type: 'it', href: '/wallet/it/intent/i1', txHash: '0xabc', chainId: 146 });

    expect(opened).toBe(true);
    expect(router.push).toHaveBeenCalledWith('/wallet/it/intent/i1');
    expect(Linking.openURL).not.toHaveBeenCalled();
  });

  it('falls back to the explorer link', () => {
    const hash = `0x${'ab'.repeat(32)}`;
    const opened = openActivityItem({ ...base, txHash: hash, chainId: 146 });

    expect(opened).toBe(true);
    expect(router.push).not.toHaveBeenCalled();
    expect(Linking.openURL).toHaveBeenCalledWith(expect.stringContaining(hash));
  });

  it('does nothing for an item with neither', () => {
    expect(openActivityItem(base)).toBe(false);
    expect(router.push).not.toHaveBeenCalled();
    expect(Linking.openURL).not.toHaveBeenCalled();
  });
});

describe('ActivityRow', () => {
  it('is not tappable without a screen or an explorer link', () => {
    const row = render(base).root.findByProps({ testID: 'row' });
    expect(row.props.disabled).toBe(true);
  });

  it('is tappable with an in-app screen', () => {
    const row = render({ ...base, href: '/wallet/it/intent/i1' }).root.findByProps({ testID: 'row' });
    expect(row.props.disabled).toBe(false);
  });

  it('shows a pre-resolved status with its own label and tone', () => {
    const badge = render({
      ...base,
      status: 'Recovering',
      statusVisual: { label: 'Recovering', tone: 'warning' },
    }).root.findByProps({ testID: 'badge' });

    expect(badge.props.children).toBe('Recovering');
    expect(badge.props.style.color).toBe('tone:warning');
  });

  it('keeps the raw-status badge for everything else', () => {
    const badge = render({ ...base, status: 'pending_payment' }).root.findByProps({ testID: 'badge' });

    expect(badge.props.children).toBe('pending payment');
    expect(badge.props.style.color).toBe('color:warning');
  });
});
