// utils/fundingPicker.ts
//
// What "Pay with another token" at checkout may offer, as an AssetPicker
// `allow`: any coin she holds on an EVM network, except
//   - wallet MCGP (never used to pay — owner decision 2026-09-28; the server
//     refuses it too), and
//   - the order token itself on Sonic — that is a direct payment, not a swap.
// Non-EVM networks are left out: the funding swap is signed by her EVM wallet,
// which is also where the order token must land.
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import { SONIC_CHAIN_ID, SONIC_ORDER_TOKENS } from '@/services/checkoutFundingRun';
import { notWalletMcgp } from '@/utils/walletMcgp';

export function fundingPickerAllow(orderToken: string): (chainId: number, token?: LiFiToken) => boolean {
  const own = SONIC_ORDER_TOKENS[orderToken.trim().toUpperCase()];
  return (chainId, token) => {
    if (isNonEVMLiFiChain(chainId)) return false;
    if (!notWalletMcgp(chainId, token)) return false;
    if (chainId === SONIC_CHAIN_ID && own && token?.address?.toLowerCase() === own) return false;
    return true;
  };
}
