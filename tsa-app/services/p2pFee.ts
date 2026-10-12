// services/p2pFee.ts
//
// Fee display maths. The rate ALWAYS comes from the API (live FeeManager rate
// or the trade's snapshot) — there is no hardcoded fallback; unknown → null and
// the screen shows neutral copy. The fee comes out of the crypto RECEIVER.
// Rounding copies P2PEscrowV3.sol (`amount * bps / 10000`, floor).
import type { P2PChainInfo } from './p2p';

const BPS_DENOM = 10_000n;

export interface FeeSplit {
  fee: string;
  net: string;
}

function validBps(bps: number | null | undefined): bps is number {
  return typeof bps === 'number' && Number.isInteger(bps) && bps >= 0 && bps <= 10_000;
}

export function feeSplit(grossBaseUnits: string, feeBps: number | null | undefined): FeeSplit | null {
  if (!validBps(feeBps)) return null;
  let gross: bigint;
  try {
    gross = BigInt(grossBaseUnits || '0');
  } catch {
    return null;
  }
  if (gross < 0n) return null;
  const fee = (gross * BigInt(feeBps)) / BPS_DENOM;
  return { fee: fee.toString(), net: (gross - fee).toString() };
}

export function formatFeePct(feeBps: number | null | undefined): string | null {
  if (!validBps(feeBps)) return null;
  return `${Number((feeBps / 100).toFixed(2))}%`;
}

export function feeBpsFor(chains: P2PChainInfo[], chainId: number | undefined, token: string): number | null {
  const sym = token.toUpperCase();
  const chain = chainId != null
    ? chains.find((c) => c.chainId === chainId)
    : chains.find((c) => c.assets.some((a) => a.toUpperCase() === sym));
  const v = chain?.feeBps?.[sym];
  return validBps(v) ? v : null;
}

export function tradeFee(t: {
  tokenAmount: string;
  feeBps?: number | null;
  feeAmount?: string | null;
  buyerNetAmount?: string | null;
}): FeeSplit | null {
  const { feeAmount: fee, buyerNetAmount: net } = t;
  // Trust the server's figures only when they are plain base-unit integers
  // that add up to the gross; anything else is recomputed from the bps.
  if (fee && net && /^\d+$/.test(fee) && /^\d+$/.test(net) && /^\d+$/.test(t.tokenAmount)
    && BigInt(fee) + BigInt(net) === BigInt(t.tokenAmount)) {
    return { fee, net };
  }
  return feeSplit(t.tokenAmount, t.feeBps);
}
