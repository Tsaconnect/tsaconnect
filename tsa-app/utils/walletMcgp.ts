// utils/walletMcgp.ts
//
// Owner decision 2026-09-28: the plain ERC-20 MCGP in a user's own wallet is
// never used to pay or sold for naira. The only usable MCGP is Spendable MCGP
// (the entitlement balance), sold from the MCGP row. Wallet MCGP is still shown,
// sent and swapped in All Swap, so this is applied only by the TSA Pay and naira
// sell pickers — never the shared asset catalog. The server refuses it too
// (ErrSellWalletMCGP).
//
// Matched by chain and contract address, never by symbol: anyone can deploy a
// token called "MCGP". The address comes from the one list of contracts the
// app vouches for (utils/tokenLogo.ts).
import type { LiFiToken } from '@/services/lifi';
import { isKnownContract } from './tokenLogo';

export function isWalletMcgp(chainId: number, token?: Pick<LiFiToken, 'address'>): boolean {
  return isKnownContract(chainId, token?.address, 'MCGP');
}

/** An AssetPicker `allow` that offers everything except wallet MCGP. */
export function notWalletMcgp(chainId: number, token?: LiFiToken): boolean {
  return !isWalletMcgp(chainId, token);
}
