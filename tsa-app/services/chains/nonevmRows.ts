// services/chains/nonevmRows.ts
//
// Turns a /wallet/nonevm balance into the wallet's display row — one mapper for
// Tron, Solana, Sui and Bitcoin. Kept apart from nonevmApi (which pulls in the
// HTTP client and storage) so the wallets and their tests can share it.
import { formatUnits } from 'ethers';
import { NONEVM_CHAINS } from '../../constants/chains';
import type { NonEVMBalance } from './nonevmApi';
import type { WalletBalanceWithChain } from '../walletApi';

/**
 * The USD price for a balance row: the backend's when it sent one, else a $1
 * pin for OUR stablecoins only. Pinning by symbol alone would price an
 * airdropped coin that calls itself "USDC" as real USDC.
 */
export function nonEvmUsdPrice(b: NonEVMBalance): number {
  if (typeof b.usdPrice === 'number') return b.usdPrice;
  if (!b.isNative && !b.listed) return 0;
  return b.symbol === 'USDT' || b.symbol === 'USDC' ? 1 : 0;
}

/** A balance as a display row. `name` defaults to the symbol. */
export function toNonEvmBalanceRow(
  b: NonEVMBalance,
  chainKey: keyof typeof NONEVM_CHAINS,
  name: string = b.symbol,
): WalletBalanceWithChain {
  const human = formatUnits(b.raw || '0', b.decimals);
  const usdPrice = nonEvmUsdPrice(b);
  const usdValue = typeof b.usdValue === 'number' ? b.usdValue : parseFloat(human) * usdPrice;
  return {
    symbol: b.symbol,
    name,
    balance: human,
    usdPrice,
    usdValue: usdValue.toFixed(2),
    contractAddress: b.tokenAddress || '',
    decimals: b.decimals,
    // Passed through as the API said them, so nothing downstream has to infer.
    native: b.isNative ? true : undefined,
    listed: b.listed === true ? true : undefined,
    chainKey,
    chainName: NONEVM_CHAINS[chainKey].shortName,
  };
}
