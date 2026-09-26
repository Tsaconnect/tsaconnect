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
jest.mock('./Spinner', () => ({ __esModule: true, Spinner: () => 'spinner' }));
jest.mock('./Text', () => ({
  __esModule: true,
  Text: ({ children, color, weight }: { children: React.ReactNode; color?: string; weight?: string }) =>
    `[${color ?? ''}|${weight ?? ''}]${children}`,
}));

// eslint-disable-next-line import/first -- jest hoists the mocks above the imports they replace.
import React from 'react';
// eslint-disable-next-line import/first
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
// eslint-disable-next-line import/first
import { Stepper, type Step } from './Stepper';

const render = (steps: Step[]) => {
  let r!: ReactTestRenderer;
  act(() => { r = TestRenderer.create(<Stepper steps={steps} />); });
  return JSON.stringify(r.toJSON());
};

describe('Stepper', () => {
  it('draws each state with its own marker', () => {
    const out = render([
      { key: 'a', label: 'Sent', state: 'done' },
      { key: 'b', label: 'Paying', state: 'active' },
      { key: 'c', label: 'Stuck', state: 'failed' },
      { key: 'd', label: 'Paid', state: 'pending' },
    ]);
    expect(out).toContain('icon:check');
    expect(out).toContain('spinner');
    expect(out).toContain('icon:close');
    expect(out).toContain('[danger|700]Stuck');
    expect(out).toContain('[textMuted|500]Paid');
  });

  it('emphasises the current step of a history, and shows each detail', () => {
    const out = render([
      { key: 'a', label: 'Placed', state: 'done', detail: 'Sep 25' },
      { key: 'b', label: 'Shipped', state: 'done', current: true },
    ]);
    expect(out).toContain('[textPrimary|500]Placed');
    expect(out).toContain('[textPrimary|700]Shipped');
    expect(out).toContain('Sep 25');
  });

  it('says each step and its state to a screen reader, not only by icon and colour', () => {
    let r!: ReactTestRenderer;
    act(() => {
      r = TestRenderer.create(
        <Stepper
          steps={[
            { key: 'a', label: 'Sent', state: 'done', detail: 'Sep 25' },
            { key: 'b', label: 'Paying', state: 'active' },
            { key: 'c', label: 'Paid', state: 'pending' },
            { key: 'd', label: 'Stuck', state: 'failed' },
          ]}
        />,
      );
    });
    const labels = r.root
      .findAll((n) => typeof (n as { type?: unknown }).type === 'string' && n.props.accessible === true)
      .map((n) => n.props.accessibilityLabel);
    expect(labels).toEqual(['Sent, done, Sep 25', 'Paying, in progress', 'Paid, not started', 'Stuck, failed']);
  });

  it('joins steps with a rail, and none after the last', () => {
    const one = render([{ key: 'a', label: 'Only', state: 'done' }]);
    const two = render([{ key: 'a', label: 'A', state: 'done' }, { key: 'b', label: 'B', state: 'pending' }]);
    const rails = (s: string) => (s.match(/"width":2,"flex":1/g) ?? []).length;
    expect(rails(one)).toBe(0);
    expect(rails(two)).toBe(1);
  });
});
