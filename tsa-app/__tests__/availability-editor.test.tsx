/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Listing hours (2026-11-02 listing-form UX pass): a new listing starts open
 * every day, and one "Every day" switch opens or closes the whole week without
 * losing the hours of days already set.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null), removeItem: jest.fn(async () => null) },
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
    type: new Proxy({}, { get: () => ({ fontSize: 14, lineHeight: 20, weight: '400' }) }),
    fonts: new Proxy({}, { get: () => 'System' }),
  }),
}));

jest.mock('@/components/ui', () => {
  const { View, Text: RNText, Switch } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Card: ({ children }: any) => <View>{children}</View>,
    Text: ({ children }: any) => <RNText>{children}</RNText>,
    Toggle: (props: any) => <Switch {...props} />,
    Button: (props: any) => <View {...props} />,
    IconButton: () => null,
    Input: (props: any) => <View {...props} />,
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import AvailabilityEditor, { everyDayWindows } from '@/components/services/AvailabilityEditor';

describe('everyDayWindows', () => {
  it('opens all seven days, 09:00 to 17:00', () => {
    const windows = everyDayWindows();
    expect(windows.map((w) => w.dayOfWeek)).toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(windows.every((w) => w.startTime === '09:00' && w.endTime === '17:00')).toBe(true);
  });
});

const everyDaySwitch = (tree: ReactTestRenderer) =>
  tree.root.findAll((n) => n.props.accessibilityLabel === 'Toggle every day' && n.props.onValueChange)[0];

const mount = (windows: any[], onChange: jest.Mock) => {
  let tree!: ReactTestRenderer;
  act(() => {
    tree = TestRenderer.create(<AvailabilityEditor windows={windows} onChange={onChange} />);
  });
  return tree;
};

describe('AvailabilityEditor every-day switch', () => {
  it('opens the missing days and keeps the hours already set', () => {
    const onChange = jest.fn();
    const monday = { dayOfWeek: 1, startTime: '07:00', endTime: '20:00' };
    const tree = mount([monday], onChange);

    act(() => everyDaySwitch(tree).props.onValueChange(true));

    const next = onChange.mock.calls[0][0];
    expect(next).toHaveLength(7);
    expect(next.filter((w: any) => w.dayOfWeek === 1)).toEqual([monday]);
  });

  it('closes the whole week', () => {
    const onChange = jest.fn();
    const tree = mount(everyDayWindows(), onChange);

    expect(everyDaySwitch(tree).props.value).toBe(true);
    act(() => everyDaySwitch(tree).props.onValueChange(false));

    expect(onChange).toHaveBeenCalledWith([]);
  });
});

describe('AvailabilityEditor same hours every day', () => {
  const inputs = (tree: ReactTestRenderer) =>
    // Host nodes only: the mocked Input and the View it renders share props.
    tree.root.findAll((n) => typeof (n as any).type === 'string' && n.props.label === 'From');

  it('shows one row of hours that sets every day', () => {
    const onChange = jest.fn();
    const tree = mount(everyDayWindows(), onChange);

    expect(inputs(tree)).toHaveLength(1);
    act(() => inputs(tree)[0].props.onChangeText('07:00'));

    const next = onChange.mock.calls[0][0];
    expect(next).toHaveLength(7);
    expect(next.every((w: any) => w.startTime === '07:00' && w.endTime === '17:00')).toBe(true);
  });

  it('expands to one card per day on request', () => {
    const tree = mount(everyDayWindows(), jest.fn());
    const perDay = tree.root.findAll((n) => typeof (n as any).type === 'string' && n.props.title === 'Set different hours per day')[0];

    act(() => perDay.props.onPress());

    expect(inputs(tree)).toHaveLength(7);
  });

  it('shows day cards when the hours differ', () => {
    const windows = everyDayWindows().map((w) => (w.dayOfWeek === 6 ? { ...w, endTime: '13:00' } : w));
    const tree = mount(windows, jest.fn());

    expect(inputs(tree)).toHaveLength(7);
  });
});
