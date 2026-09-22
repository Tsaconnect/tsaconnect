// services/chains/nonevmPrice.ts
//
// Pricing for non-EVM balance rows, kept apart from nonevmApi (which pulls in
// the HTTP client and storage) so the wallet mappers and their tests share it.
import type { NonEVMBalance } from './nonevmApi';

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
