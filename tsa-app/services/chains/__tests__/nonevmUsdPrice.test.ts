import { nonEvmUsdPrice } from '../nonevmPrice';
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
