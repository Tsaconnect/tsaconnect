/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * The notice is the only thing telling a payer that the company account credits
 * them by their own name and does not read "TSA Connect". It must show the
 * server's wording exactly and announce itself to a screen reader.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: { warning: '#F59E0B', warningBg: '#F59E0B1F' },
    spacing: { xs: 4, sm: 8, md: 12 },
    radius: { md: 12 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(Text, null, props.children),
    Icon: () => null,
  };
});

import React from 'react';
import { Text } from 'react-native';
import { PayerInstructionNotice } from '../PayerInstructionNotice';

// The repo ships no @types/react-test-renderer, so it is loaded untyped rather
// than adding a TS7016 error to `tsc --noEmit`.
// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');

const INSTRUCTION =
  'Pay from a bank account in your own name. The account name reads SOME COMPANY LTD, not TSA Connect — that is us.';

function render(instruction: string): any {
  let tree: any;
  TestRenderer.act(() => {
    tree = TestRenderer.create(<PayerInstructionNotice instruction={instruction} />);
  });
  return tree;
}

describe('PayerInstructionNotice', () => {
  it('renders the server wording verbatim', () => {
    const tree = render(INSTRUCTION);
    const texts = tree.root.findAllByType(Text).map((node: any) => node.props.children);
    expect(texts).toContain(INSTRUCTION);
  });

  it('announces itself as an alert carrying the full instruction', () => {
    const tree = render(INSTRUCTION);
    const box = tree.root.findByProps({ testID: 'payer-instruction-notice' });
    expect(box.props.accessibilityRole).toBe('alert');
    expect(box.props.accessibilityLabel).toContain(INSTRUCTION);
  });
});
