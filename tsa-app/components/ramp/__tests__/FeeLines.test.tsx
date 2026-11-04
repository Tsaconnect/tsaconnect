/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000' }), spacing: { xs: 4, sm: 8, md: 12 } }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return { __esModule: true, Text: (props: any) => react.createElement(Text, null, props.children) };
});

import React from 'react';
import { Text } from 'react-native';
import { FeeLines } from '../FeeLines';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');

function texts(el: React.ReactElement): string {
  let tree: any;
  TestRenderer.act(() => {
    tree = TestRenderer.create(el);
  });
  return tree.root.findAllByType(Text).map((n: any) => String(n.props.children)).join(' | ');
}

describe('FeeLines', () => {
  it('shows each fee as its own line, tagged with the provider (spec §14)', () => {
    const out = texts(
      <FeeLines
        token="USDC"
        symbol="₦"
        fees={[
          { label: 'Provider conversion fee (1.5%)', provider: 'Flutterwave', token: '1.46216', ngn: '2474.80' },
          { kind: 'network', label: 'Network fee', provider: 'Flutterwave', token: '1.5', ngn: '2538.85' },
        ]}
      />,
    );
    expect(out).toContain('Provider conversion fee (1.5%)');
    expect(out).toContain('Network fee');
    // Never a third-party name: "via provider" only where the label doesn't already say it.
    expect(out).not.toMatch(/Flutterwave|LiFi/);
    expect(out.match(/via provider/g)?.length).toBe(1);
    expect(out).toContain('−1.46216 USDC | ≈ ₦2,474.80');
    expect(out).toContain('−1.5 USDC');
    expect(out).not.toContain('₦2,538.85'); // a network fee is shown in the token alone
  });

  it('rewrites a provider name in a label stored before the rule', () => {
    const out = texts(
      <FeeLines
        token="USDC"
        symbol="₦"
        fees={[
          { label: 'Flutterwave conversion fee (1.5%)', provider: 'Flutterwave', token: '0.2', ngn: '280' },
          { label: 'Conversion (LiFi)', provider: 'LiFi', token: '0.3', ngn: '420' },
        ]}
      />,
    );
    expect(out).toContain('Provider conversion fee (1.5%)');
    expect(out).toContain('Conversion (provider)');
    expect(out).not.toMatch(/Flutterwave|LiFi|via provider/);
  });

  it('renders nothing without fees', () => {
    expect(texts(<FeeLines token="USDC" symbol="₦" fees={[]} />)).toBe('');
  });
});
