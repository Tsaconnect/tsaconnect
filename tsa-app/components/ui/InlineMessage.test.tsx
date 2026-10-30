jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: (_t, key) => `color:${String(key)}` }),
    radius: new Proxy({}, { get: () => 12 }),
    spacing: new Proxy({}, { get: () => 8 }),
    isDark: false,
  }),
}));
jest.mock('./Icon', () => ({ __esModule: true, Icon: ({ name }: { name: string }) => `icon:${name}` }));
jest.mock('./Text', () => ({
  __esModule: true,
  Text: ({ children, color }: { children: React.ReactNode; color?: string }) => `[${color ?? ''}]${children}`,
}));

// eslint-disable-next-line import/first -- jest hoists the mocks above the imports they replace.
import React from 'react';
// eslint-disable-next-line import/first
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
// eslint-disable-next-line import/first
import { InlineMessage, type InlineMessageProps } from './InlineMessage';

const render = (props: InlineMessageProps) => {
  let r!: ReactTestRenderer;
  act(() => { r = TestRenderer.create(<InlineMessage {...props} />); });
  return r;
};
const text = (r: ReactTestRenderer) => JSON.stringify(r.toJSON());
// The first element that takes a press and matches; `label` undefined finds the unlabelled action.
const pressable = (r: ReactTestRenderer, label?: string) =>
  r.root.findAll((n) => typeof n.props.onPress === 'function' && n.props.accessibilityLabel === label)[0];

describe('InlineMessage', () => {
  it('renders nothing without a message, so error state can be passed straight in', () => {
    expect(render({ message: null }).toJSON()).toBeNull();
    expect(render({ message: '' }).toJSON()).toBeNull();
  });

  it('shows the whole message in the tone colour and announces it', () => {
    const long = 'This link cannot be cancelled because a customer has already started paying it. '.repeat(3);
    const r = render({ message: long });
    expect(text(r)).toContain(`[danger]${long}`);
    expect(r.root.findAllByProps({ accessibilityRole: 'alert' })[0].props.accessibilityLiveRegion).toBe('assertive');
  });

  it('a warning and an info each use their own colour, and info is announced politely', () => {
    expect(text(render({ message: 'w', tone: 'warning' }))).toContain('[warning]w');
    const info = render({ message: 'i', tone: 'info' });
    expect(text(info)).toContain('[info]i');
    expect(info.root.findAllByProps({ accessibilityRole: 'alert' })[0].props.accessibilityLiveRegion).toBe('polite');
  });

  it('a title leads and the message reads as body text', () => {
    const out = text(render({ message: 'Fund your wallet', title: 'Not enough gas' }));
    expect(out).toContain('[danger]Not enough gas');
    expect(out).toContain('[textPrimary]Fund your wallet');
  });

  it('offers dismiss and a recovery action only when given handlers', () => {
    expect(pressable(render({ message: 'm' }), 'Dismiss')).toBeUndefined();

    const onDismiss = jest.fn();
    const onAction = jest.fn();
    const r = render({ message: 'm', onDismiss, actionLabel: 'Try again', onAction });
    expect(text(r)).toContain('Try again');
    act(() => pressable(r, 'Dismiss').props.onPress());
    expect(onDismiss).toHaveBeenCalled();
    act(() => pressable(r).props.onPress());
    expect(onAction).toHaveBeenCalled();
  });
});
