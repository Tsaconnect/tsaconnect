/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * TokenAvatar is the app's one token icon. What it must keep doing: show the
 * registry logo for a curated token, fall back to a letter when THAT logo fails
 * without giving up on the next token's logo, never lend a registry logo to an
 * uncurated token, and badge the chain except on the chain's own coin.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: { surface: '#fff', surfaceAlt: '#eee', textMuted: '#999' } }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return { __esModule: true, Text: (props: any) => react.createElement(Text, null, props.children) };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import TokenAvatar from '../TokenAvatar';

type Props = React.ComponentProps<typeof TokenAvatar>;

function render(props: Props): ReactTestRenderer {
  let tree: ReactTestRenderer | undefined;
  act(() => {
    tree = TestRenderer.create(<TokenAvatar {...props} />);
  });
  return tree!;
}

/** Whether an element with this testID rendered. */
const rendered = (tree: ReactTestRenderer, testID: string) =>
  tree.root.findAllByProps({ testID }).length > 0;

describe('TokenAvatar', () => {
  it('shows the registry logo for a curated token', () => {
    const tree = render({ symbol: 'USDT' });
    expect(rendered(tree, 'token-avatar-logo')).toBe(true);
    expect(rendered(tree, 'token-avatar-letter')).toBe(false);
  });

  it('falls back to a letter when the logo fails, and tries the next token’s logo', () => {
    const tree = render({ symbol: 'USDT' });
    act(() => {
      tree.root.findAllByProps({ testID: 'token-avatar-logo' })[0].props.onError();
    });
    expect(rendered(tree, 'token-avatar-letter')).toBe(true);

    act(() => {
      tree.update(<TokenAvatar symbol="USDC" />);
    });
    expect(rendered(tree, 'token-avatar-logo')).toBe(true);
  });

  it('never lends a registry logo to an uncurated token', () => {
    const tree = render({ symbol: 'USDT', curated: false });
    expect(rendered(tree, 'token-avatar-logo')).toBe(false);
    expect(rendered(tree, 'token-avatar-letter')).toBe(true);
  });

  it('uses the bundled MCGP logo', () => {
    expect(rendered(render({ symbol: 'MCGP' }), 'token-avatar-logo')).toBe(true);
  });

  it('badges the chain, except on the chain’s own coin', () => {
    expect(rendered(render({ symbol: 'USDT', chainKey: 'bsc' }), 'token-avatar-chain')).toBe(true);
    expect(rendered(render({ symbol: 'BNB', chainKey: 'bsc' }), 'token-avatar-chain')).toBe(false);
  });
});
