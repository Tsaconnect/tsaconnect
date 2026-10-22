// services/sellerPayoutApi.ts
//
// Where a marketplace seller is paid when a buyer confirms an order: a bank
// account (in naira), or none (their wallet). Changing it is guarded like a TSA
// Pay payout change: the account password, then a code emailed to them.
//   GET    /api/seller/payout-method
//   PUT    /api/seller/payout-method                 (checks the account, emails a code)
//   POST   /api/seller/payout-method/:id/confirm     (saves it)
//   DELETE /api/seller/payout-method                 (back to the wallet)
import { createApiRequest } from '@/services/apiRequest';

export type SellerPayout = {
  /** 'wallet' when none is set. */
  kind: 'wallet' | 'bank' | 'mobile_money';
  currency?: string;
  institutionCode?: string;
  institutionName?: string;
  accountNumber?: string;
  accountName?: string;
};

/** The change waits for this code, emailed to sentTo. */
export type PayoutConfirmation = { id: string; expiresAt: string; sentTo: string };

/** Machine codes the payout routes answer with. */
export const PAYOUT_CODES = {
  PASSWORD_INCORRECT: 'password_incorrect',
  KYC_REQUIRED: 'kyc_verification_required',
  NAME_MISMATCH: 'account_name_mismatch',
  UNVERIFIED: 'account_unverified',
  CODE_INVALID: 'code_invalid',
  NO_PENDING: 'no_pending_change',
} as const;

const request = createApiRequest({
  prefix: '/seller',
  unavailableMessage: 'Payout settings are not available right now. Please try again later.',
  networkMessage: 'Could not reach the server. Check your connection and try again.',
});

export const getPayoutMethod = (): Promise<SellerPayout> => request<SellerPayout>('/payout-method');

export async function requestPayoutMethod(
  institutionCode: string,
  accountNumber: string,
  password: string,
): Promise<PayoutConfirmation> {
  const answer = await request<{ confirmation: PayoutConfirmation }>('/payout-method', {
    method: 'PUT',
    body: JSON.stringify({ currency: 'NGN', institutionCode, accountNumber, password }),
  });
  return answer.confirmation;
}

export const confirmPayoutMethod = (changeId: string, code: string): Promise<SellerPayout> =>
  request<SellerPayout>(`/payout-method/${encodeURIComponent(changeId)}/confirm`, {
    method: 'POST',
    body: JSON.stringify({ code: code.trim() }),
  });

export const clearPayoutMethod = (): Promise<SellerPayout> =>
  request<SellerPayout>('/payout-method', { method: 'DELETE' });
