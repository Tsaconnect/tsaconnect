/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
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
          { label: 'Flutterwave conversion fee (1.5%)', provider: 'Flutterwave', token: '1.46216', ngn: '2474.80' },
          { label: 'Network fee', provider: 'Flutterwave', token: '1.5', ngn: '2538.85' },
        ]}
      />,
    );
    expect(out).toContain('Flutterwave conversion fee (1.5%)');
    expect(out).toContain('Network fee');
    expect(out.match(/Flutterwave \|/g)?.length ?? 0).toBeGreaterThanOrEqual(1);
    expect(out).toContain('−1.46216 USDC (≈ ₦2,474.80)');
    expect(out).toContain('−1.5 USDC (≈ ₦2,538.85)');
  });

  it('renders nothing without fees', () => {
    expect(texts(<FeeLines token="USDC" symbol="₦" fees={[]} />)).toBe('');
  });
});
