import { nonEvmUsdPrice, toNonEvmBalanceRow } from '../nonevmRows';
import type { NonEVMBalance } from '../nonevmApi';

const bal = (over: Partial<NonEVMBalance>): NonEVMBalance => ({
  symbol: 'USDC', raw: '1000000', decimals: 6, isNative: false, ...over,
});

describe('nonEvmUsdPrice', () => {
  it("uses the backend's price when it sent one", () => {
    expect(nonEvmUsdPrice(bal({ usdPrice: 0.99 }))).toBe(0.99);
    expect(nonEvmUsdPrice(bal({ usdPrice: 0, listed: true }))).toBe(0);
  });

  it('pins only our own stablecoins to $1', () => {
    expect(nonEvmUsdPrice(bal({ listed: true }))).toBe(1);
    // An airdropped coin calling itself USDC is worth nothing we can vouch for.
    expect(nonEvmUsdPrice(bal({}))).toBe(0);
    expect(nonEvmUsdPrice(bal({ symbol: 'SOL', isNative: true }))).toBe(0);
  });
});

describe('toNonEvmBalanceRow', () => {
  it("passes the API's native and listed flags through instead of leaving them to be guessed", () => {
    const sol = toNonEvmBalanceRow(bal({ symbol: 'SOL', raw: '1000000000', decimals: 9, isNative: true, usdPrice: 150 }), 'solana');
    expect(sol).toMatchObject({ native: true, balance: '1.0', usdValue: '150.00', chainKey: 'solana' });
    const usdc = toNonEvmBalanceRow(bal({ tokenAddress: 'EPjF', listed: true }), 'solana');
    expect(usdc).toMatchObject({ native: undefined, listed: true, contractAddress: 'EPjF', usdValue: '1.00' });
  });
});
