// services/payMerchantApi.ts
//
// A TSA Pay merchant's own account, from the app: where they are paid
// (GET /api/pay/merchant), changing it (PUT /api/pay/merchant/settlement), and
// confirming a crypto change with the code emailed to them
// (POST /api/pay/merchant/actions/:id/confirm). The payer's side of TSA Pay is
// services/payApi.ts; the merchant's server API (/api/pay/v1, key-authenticated)
// is never called from the app.
import { createApiRequest } from '@/services/apiRequest';
import { PayError } from '@/services/payApi';

export type SettlementMode = 'fiat' | 'crypto';

/** The owner's view: the account number arrives masked (last four digits). */
export type OwnMerchant = {
  id: string;
  businessName: string;
  status: 'active' | 'suspended';
  /** What a change from this screen is made against: a stale screen is refused. */
  version: number;
  settlementMode: SettlementMode | '';
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  chainId?: number;
  tokenAddress?: string;
  tokenSymbol?: string;
  walletAddress?: string;
};

/** A crypto change names no symbol: the server takes the listed stablecoin's. */
export type SettlementChange =
  | { settlementMode: 'fiat'; bankCode: string; accountNumber: string }
  | { settlementMode: 'crypto'; chainId: number; tokenAddress: string; walletAddress: string };

/** A guarded change (a crypto wallet) waits for the code emailed to the owner. */
export type PendingConfirmation = { id: string; kind: string; expiresAt: string; sentTo: string };

/** A change was applied (merchant), or is waiting on the emailed code (confirmation). */
export type SettlementAnswer = { merchant?: OwnMerchant; confirmation?: PendingConfirmation };

/** Machine codes the owner routes answer with — keyed on, never the words beside them. */
export const OWNER_CODES = {
  NO_MERCHANT: 'no_merchant',
  NAME_MISMATCH: 'name_mismatch',
  PASSWORD_INCORRECT: 'password_incorrect',
  ACCOUNT_LOCKED: 'account_locked',
  STALE: 'merchant_stale',
  NO_PENDING: 'no_pending_change',
  CODE_INVALID: 'code_invalid',
} as const;

const request = createApiRequest({
  prefix: '/pay/merchant',
  unavailableMessage: 'Payment settings are not available right now. Please try again later.',
  networkMessage: 'Could not reach the server. Check your connection and try again.',
  makeError: (message, status, code) => new PayError(message, status, code),
});

/** Her merchant, or a PayError coded no_merchant when she has none. */
export function getOwnMerchant(): Promise<OwnMerchant> {
  return request<OwnMerchant>('');
}

/** Change where she is paid. The password is checked again on the server. */
export function updateSettlement(change: SettlementChange, password: string, expectedVersion: number): Promise<SettlementAnswer> {
  return request<SettlementAnswer>('/settlement', {
    method: 'PUT',
    body: JSON.stringify({ ...change, password, expectedVersion }),
  });
}

/** Apply a waiting guarded change with the code from her email. */
export function confirmOwnerAction(actionId: string, code: string): Promise<SettlementAnswer> {
  return request<SettlementAnswer>(`/actions/${encodeURIComponent(actionId)}/confirm`, {
    method: 'POST',
    body: JSON.stringify({ code: code.trim() }),
  });
}
