// utils/payPick.ts
//
// Which coins the pay screen's picker offers. Nothing can be sent from Solana,
// Bitcoin or Sui for a payment yet (the server refuses to price a route signed
// there), and a crypto-settling merchant's own token on their own chain can't
// go through the swap route — the server refuses it too (ErrPaySameToken), so
// it is hidden rather than offered and rejected. Wallet MCGP never pays
// (utils/walletMcgp.ts), for any merchant — the server refuses it as well.
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import type { PayCheckout } from '@/services/payApi';
import { isWalletMcgp } from './walletMcgp';

export function payPickFilter(
  checkout: Pick<PayCheckout, 'settlementMode' | 'settleChainId' | 'settleTokenAddress'>,
): (chainId: number, token?: LiFiToken) => boolean {
  return (chainId, token) => {
    if (isNonEVMLiFiChain(chainId)) return false;
    if (isWalletMcgp(chainId, token)) return false;
    if (checkout.settlementMode !== 'crypto' || !token) return true;
    const own =
      chainId === checkout.settleChainId &&
      !!checkout.settleTokenAddress &&
      token.address.toLowerCase() === checkout.settleTokenAddress.toLowerCase();
    return !own;
  };
}
