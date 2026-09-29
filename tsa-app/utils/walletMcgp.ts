// utils/walletMcgp.ts
//
// Owner decision 2026-09-28: the plain ERC-20 MCGP in a user's own wallet is
// never used to pay or sold for naira. The only usable MCGP is Spendable MCGP
// (the entitlement balance), sold from the MCGP row. Wallet MCGP is still shown,
// sent and swapped in All Swap, so `notWalletMcgp` is applied only by the TSA
// Pay and naira sell pickers. The shared asset catalog only changes which MCGP
// balance its receiving side displays (`withSpendableMcgp`). The server refuses it too
// (ErrSellWalletMCGP).
//
// Matched by chain and contract address, never by symbol: anyone can deploy a
// token called "MCGP". The address comes from the one list of contracts the
// app vouches for (utils/tokenLogo.ts).
import type { LiFiToken } from '@/services/lifi';
import type { Holding } from '@/services/assetCatalog';
import { DEFAULT_TOKENS } from '@/constants/chains';
import { isKnownContract, MCGP_CONTRACT } from './tokenLogo';

/** Spendable MCGP lives in the entitlement contract on Sonic mainnet, whatever the app's toggle says. */
const SPENDABLE_MCGP_CHAIN_ID = 146;

export function isWalletMcgp(chainId: number, token?: Pick<LiFiToken, 'address'>): boolean {
  return isKnownContract(chainId, token?.address, 'MCGP');
}

/** An AssetPicker `allow` that offers everything except wallet MCGP. */
export function notWalletMcgp(chainId: number, token?: LiFiToken): boolean {
  return !isWalletMcgp(chainId, token);
}

/**
 * The balances a coin list shows, with MCGP counted as Spendable MCGP — the
 * only MCGP usable in the app — in place of wallet MCGP. `spendable` null
 * (not read yet, or unreadable) shows no MCGP balance rather than the wallet's.
 *
 * For what a list DISPLAYS only: a flow that moves the wallet's own tokens
 * (a sell, a checkout funding) must keep reading the real wallet balances.
 */
export function withSpendableMcgp(
  holdings: Holding[],
  spendable: { balance: number; usdValue: number } | null,
): Holding[] {
  const rest = holdings.filter(h => !isWalletMcgp(h.chainId, h));
  if (!spendable || !(spendable.balance > 0)) return rest;
  return [...rest, {
    chainId: SPENDABLE_MCGP_CHAIN_ID,
    address: MCGP_CONTRACT,
    native: false,
    trusted: true,
    symbol: DEFAULT_TOKENS.MCGP.symbol,
    name: DEFAULT_TOKENS.MCGP.name,
    decimals: DEFAULT_TOKENS.MCGP.decimals,
    balance: spendable.balance,
    usdValue: spendable.usdValue,
  }];
}
