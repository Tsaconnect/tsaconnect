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
import type { SellerPayout } from '@/services/sellerPayoutApi';

/** What a merchant can choose to be paid in themselves; anything else is an admin's call. */
export const OWNER_SETTLEMENT_SYMBOLS = ['USDC', 'USDT'];

/**
 * A token the merchant can pick: USDC or USDT on an EVM chain this app knows.
 * The server holds the choice to the listed contract on that chain (a token
 * merely CALLED "USDC" is refused there), and never a native coin — a delivery
 * is proven from the token's Transfer log, which a native coin does not have.
 */
export function settlesIn(chainId: number, token?: Pick<LiFiToken, 'address' | 'symbol'>): boolean {
  if (!getChainByChainId(chainId)) return false;
  if (!token) return true;
  return !isNativeTokenAddress(token.address) && OWNER_SETTLEMENT_SYMBOLS.includes(token.symbol?.toUpperCase());
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
  return { change: { settlementMode: 'crypto', chainId: form.token.chainId, tokenAddress: form.token.address, walletAddress: wallet } };
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

/** The merchant's payout account when it is a naira bank account, else null: the only kind TSA Pay can be paid into from it. */
export function payoutBankOf(payout: SellerPayout | null | undefined): (SellerPayout & { institutionCode: string; accountNumber: string }) | null {
  if (payout?.kind !== 'bank' || payout.currency !== 'NGN' || !payout.institutionCode || !payout.accountNumber) return null;
  return payout as SellerPayout & { institutionCode: string; accountNumber: string };
}

/**
 * TSA Pay already pays into this payout account: the same bank, and an account
 * number ending the same way (the server masks all but the last four digits).
 */
export function paysIntoPayoutAccount(m: Pick<OwnMerchant, 'settlementMode' | 'bankCode' | 'accountNumber'>, payout: { institutionCode: string; accountNumber: string }): boolean {
  const last4 = (n?: string) => (n ?? '').slice(-4);
  return m.settlementMode === 'fiat' && m.bankCode === payout.institutionCode
    && last4(m.accountNumber).length === 4 && last4(m.accountNumber) === last4(payout.accountNumber);
}
