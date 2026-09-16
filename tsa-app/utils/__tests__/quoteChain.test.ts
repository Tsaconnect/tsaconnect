import { pickQuoteChain, type QuoteBalanceRow } from '../quoteChain';

const row = (chainKey: string, symbol: string, balance: string) =>
  ({ chainKey, symbol, balance }) as QuoteBalanceRow;

describe('pickQuoteChain', () => {
  it('goes where the user holds the most of the quote asset', () => {
    const rows = [row('bsc', 'USDT', '12.5'), row('polygon', 'USDT', '80'), row('ethereum', 'USDT', '3')];
    expect(pickQuoteChain('USDT', rows, 'bsc')).toBe('polygon');
  });

  it('ignores other assets, however large', () => {
    const rows = [row('ethereum', 'USDC', '5000'), row('bsc', 'USDT', '1')];
    expect(pickQuoteChain('usdt', rows)).toBe('bsc');
  });

  it('falls back to the listing chain when the user holds none', () => {
    const rows = [row('polygon', 'USDT', '0'), row('bsc', 'USDT', 'not a number')];
    expect(pickQuoteChain('USDT', rows, 'Ethereum')).toBe('ethereum');
  });

  it('falls back to the asset default when the listing names no chain', () => {
    expect(pickQuoteChain('USDT', [])).toBe('bsc');
    expect(pickQuoteChain('USDC', [], '  ')).toBe('base');
    expect(pickQuoteChain('DAI', [])).toBe('bsc');
  });

  it('lets the listing chain break a tie instead of row order', () => {
    const rows = [row('bsc', 'USDT', '50'), row('polygon', 'USDT', '50')];
    expect(pickQuoteChain('USDT', rows, 'polygon')).toBe('polygon');
    expect(pickQuoteChain('USDT', rows)).toBe('bsc');
  });
});
