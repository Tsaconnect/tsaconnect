import { readFileSync } from 'fs';
import { join } from 'path';

// End users never see third-party vendor names in our copy. These screens
// used to name them in rendered text; comments may still explain the plumbing.
const SCREENS: Record<string, RegExp> = {
  'app/p2p/create-ad.tsx': /bybit/i,
  'app/p2p/create-buy-ad.tsx': /nomba/i,
  'utils/stableSellQuote.ts': /flutterwave|paycrest/i,
};

function withoutComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\{\s*\/\*[\s\S]*?\*\/\s*\}/g, '')
    .split('\n')
    .map((line) => line.replace(/(^|[^:'"`])\/\/.*$/, '$1'))
    .join('\n');
}

describe('vendor names in user copy', () => {
  it.each(Object.entries(SCREENS))('%s renders no vendor name', (file, banned) => {
    const code = withoutComments(readFileSync(join(__dirname, '..', file), 'utf8'));
    expect(code).not.toMatch(banned);
  });

  it('create-ad describes the anchor as the market rate', () => {
    const code = readFileSync(join(__dirname, '..', 'app/p2p/create-ad.tsx'), 'utf8');
    expect(code).toContain('Market rate stale — verify before publishing');
    expect(code).toContain('Current market P2P sell rate:');
    expect(code).toContain('Anchor: market P2P sell');
  });

  it('create-buy-ad says the payout account pays', () => {
    const code = readFileSync(join(__dirname, '..', 'app/p2p/create-buy-ad.tsx'), 'utf8');
    expect(code).toContain('Auto-paid from your payout account');
  });

  it('the swap screen shows the route label, never the tool name', () => {
    const code = withoutComments(readFileSync(join(__dirname, '..', 'screens/all-swap.tsx'), 'utf8'));
    expect(code).not.toMatch(/quote\??\.tool/);
    expect(code).toContain('ROUTE_LABEL');
  });
});
