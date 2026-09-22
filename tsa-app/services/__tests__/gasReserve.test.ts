import { DEFAULT_SWAP_GAS_UNITS, MIN_SWAP_RESERVE_WEI, gasReserveFor, spendableNative, swapGasReserve } from '../gasReserve';

const gwei = (n: number) => BigInt(Math.round(n * 1e9));

describe('gasReserveFor', () => {
  it('budgets the default swap gas at the current price plus 25%', () => {
    // 350k gas at 10 gwei = 0.0035 ETH; +25% = 0.004375 ETH.
    expect(gasReserveFor(gwei(10))).toBe(4_375_000_000_000_000n);
  });

  it("uses the quote's own gas limit when one is known", () => {
    // 200k at 10 gwei = 0.002; +25% = 0.0025.
    expect(gasReserveFor(gwei(10), 200_000n)).toBe(2_500_000_000_000_000n);
  });

  it('stays modest on Ethereum mainnet (under ~$20 at 10 gwei, $3.5k ETH)', () => {
    const reserveEth = Number(gasReserveFor(gwei(10))) / 1e18;
    expect(reserveEth * 3500).toBeLessThan(20);
  });

  it('holds nothing back when the price or the units are unknown or zero', () => {
    expect(gasReserveFor(0n)).toBe(0n);
    expect(gasReserveFor(gwei(10), 0n)).toBe(0n);
  });

  it('defaults to a positive unit budget', () => {
    expect(DEFAULT_SWAP_GAS_UNITS > 0n).toBe(true);
  });
});

describe('spendableNative', () => {
  it('subtracts the reserve from the balance', () => {
    expect(spendableNative(10n ** 18n, 4_375_000_000_000_000n)).toBe(995_625_000_000_000_000n);
  });

  it('floors at zero when the balance cannot cover the reserve', () => {
    expect(spendableNative(1_000n, 5_000n)).toBe(0n);
  });
});

describe('swapGasReserve', () => {
  it("covers the route's own gas cost (L1 data fee included) when it beats the price estimate", () => {
    // Base: L2 gas is near free, the quote says 0.00004 ETH all-in.
    const r = swapGasReserve({ gasPriceWei: 10_000_000n, gasUnits: 300_000n, quoteGasCostWei: 40_000_000_000_000n });
    expect(r).toBe(50_000_000_000_000n); // 0.00004 * 1.25
  });

  it('never goes below the chain floor before a quote exists', () => {
    const r = swapGasReserve({ gasPriceWei: 10_000_000n, floorWei: MIN_SWAP_RESERVE_WEI.base });
    expect(r).toBe(30_000_000_000_000n);
  });

  it('keeps the price estimate when it is the largest', () => {
    expect(swapGasReserve({ gasPriceWei: gwei(10), quoteGasCostWei: 1_000n })).toBe(4_375_000_000_000_000n);
  });

  it('is zero when nothing is known', () => {
    expect(swapGasReserve({})).toBe(0n);
  });
});
