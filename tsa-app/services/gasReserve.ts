// Native-gas headroom for the swap MAX button. Selling the chain's gas token
// means the swap is paid for out of the same balance, so MAX must hold some
// back — the whole balance is never swappable. Pure bigint math; the live fee
// read lives in services/wallet.nativeGasReserve.

/**
 * Gas units budgeted before a quote exists. A native-token LiFi swap needs no
 * approve, and single-chain and bridge routes land under this. Once a quote is
 * in hand its own gasLimit is used instead.
 */
export const DEFAULT_SWAP_GAS_UNITS = 350_000n;

/**
 * Headroom on top of the current gas price (1.25x), so a fee rise between
 * tapping MAX and broadcasting does not turn the reserve into a shortfall.
 * Kept modest: on Ethereum mainnet every unit of margin is money left unswapped.
 */
const MARGIN_NUMERATOR = 5n;
const MARGIN_DENOMINATOR = 4n;

/** Native base units to hold back for `gasUnits` at `gasPriceWei`. */
export function gasReserveFor(gasPriceWei: bigint, gasUnits: bigint = DEFAULT_SWAP_GAS_UNITS): bigint {
  if (gasPriceWei <= 0n || gasUnits <= 0n) return 0n;
  return (gasPriceWei * gasUnits * MARGIN_NUMERATOR) / MARGIN_DENOMINATOR;
}

/**
 * Minimum reserve per chain key. OP-stack chains (Base, Optimism) charge an L1
 * data fee on top of L2 gas that the gas price does not include — often more
 * than the L2 execution itself — so before a quote reports the real cost, the
 * price-based estimate alone would under-reserve. 0.00003 ETH is about $0.10.
 */
export const MIN_SWAP_RESERVE_WEI: Partial<Record<string, bigint>> = {
  base: 30_000_000_000_000n,
  optimism: 30_000_000_000_000n,
};

/**
 * The reserve MAX holds back: the largest of the gas-price estimate, the
 * route's own gas cost (LiFi's estimate, which includes any L1 data fee) plus
 * the same 25% margin, and the chain's floor. Unknown inputs count as zero.
 */
export function swapGasReserve({
  gasPriceWei,
  gasUnits,
  quoteGasCostWei,
  floorWei,
}: {
  gasPriceWei?: bigint | null;
  gasUnits?: bigint;
  quoteGasCostWei?: bigint | null;
  floorWei?: bigint;
}): bigint {
  const fromPrice = gasPriceWei ? gasReserveFor(gasPriceWei, gasUnits) : 0n;
  const fromQuote = quoteGasCostWei && quoteGasCostWei > 0n
    ? (quoteGasCostWei * MARGIN_NUMERATOR) / MARGIN_DENOMINATOR
    : 0n;
  const floor = floorWei ?? 0n;
  return [fromPrice, fromQuote, floor].reduce((a, b) => (b > a ? b : a), 0n);
}

/**
 * Lamports MAX holds back when the coin being sold is SOL itself: 0.01 SOL
 * covers a swap's base and priority fees and a new token account's rent, with
 * room to spare. Solana fees are not priced by gas units, so there is no
 * quote-derived figure to use instead.
 */
export const SOLANA_SWAP_RESERVE_LAMPORTS = 10_000_000n;

/** Balance minus reserve, floored at zero so MAX never offers a negative amount. */
export function spendableNative(balanceWei: bigint, reserveWei: bigint): bigint {
  const spendable = balanceWei - reserveWei;
  return spendable > 0n ? spendable : 0n;
}
