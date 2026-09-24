// utils/paySettlement.ts
//
// The pure part of a TSA Pay merchant changing where they are paid
// (app/merchants/pay-settings.tsx): what the form may send, and how the current
// destination is described. The server re-checks all of it — the bank resolves
// the account and it must be in the merchant's own name — so these checks only
// spare her a round trip on something plainly incomplete.
import { ethers } from 'ethers';
import { getChainByChainId } from '@/constants/chains';
import { isNativeTokenAddress, type LiFiToken } from '@/services/lifi';
import type { OwnMerchant, SettlementChange } from '@/services/payMerchantApi';

/**
 * A token the merchant can be paid in: an ERC-20 on an EVM chain this app knows.
 * The chain's own coin is not offered — a delivery is proven from the token's
 * Transfer log, which a native coin does not have.
 */
export function settlesIn(chainId: number, token?: Pick<LiFiToken, 'address'>): boolean {
  if (!getChainByChainId(chainId)) return false;
  return !token || !isNativeTokenAddress(token.address);
}

export type SettlementForm =
  | { mode: 'fiat'; bankCode?: string; accountNumber: string }
  | { mode: 'crypto'; token?: LiFiToken & { chainId: number }; walletAddress: string };

/** The change to send, or the one thing still missing. */
export function settlementChangeFrom(form: SettlementForm): { change: SettlementChange } | { missing: string } {
  if (form.mode === 'fiat') {
    const account = form.accountNumber.trim();
    if (!form.bankCode) return { missing: 'Choose your bank.' };
    if (!/^\d{10}$/.test(account)) return { missing: 'Enter your 10-digit account number.' };
    return { change: { settlementMode: 'fiat', bankCode: form.bankCode, accountNumber: account } };
  }
  const wallet = form.walletAddress.trim();
  if (!form.token) return { missing: 'Choose the token you want to be paid in.' };
  if (!settlesIn(form.token.chainId, form.token)) return { missing: `You can't be paid in ${form.token.symbol} on that network.` };
  if (!ethers.isAddress(wallet)) return { missing: 'Enter a valid wallet address.' };
  return {
    change: {
      settlementMode: 'crypto', chainId: form.token.chainId, tokenAddress: form.token.address,
      tokenSymbol: form.token.symbol, walletAddress: wallet,
    },
  };
}

const shortAddress = (a: string) => (a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a);

/** Where she is paid today, in one line. */
export function describeSettlement(m: OwnMerchant, bankName?: (code: string) => string | undefined): string {
  if (m.settlementMode === 'fiat') {
    const bank = (m.bankCode && bankName?.(m.bankCode)) || 'Bank account';
    return `${m.accountName ?? ''} · ${bank} ${m.accountNumber ?? ''}`.trim();
  }
  if (m.settlementMode === 'crypto') {
    const chain = m.chainId ? getChainByChainId(m.chainId)?.name ?? `chain ${m.chainId}` : '';
    return `${m.tokenSymbol ?? ''} on ${chain} to ${shortAddress(m.walletAddress ?? '')}`;
  }
  return 'Not set up yet';
}
