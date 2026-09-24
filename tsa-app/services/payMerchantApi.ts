// services/payMerchantApi.ts
//
// A TSA Pay merchant's own account, from the app: where they are paid
// (GET /api/pay/merchant) and changing it (PUT /api/pay/merchant/settlement).
// The payer's side of TSA Pay is services/payApi.ts; the merchant's server API
// (/api/pay/v1, key-authenticated) is never called from the app.
import { createApiRequest } from '@/services/apiRequest';
import { PayError } from '@/services/payApi';

export type SettlementMode = 'fiat' | 'crypto';

/** The owner's view: the account number arrives masked (last four digits). */
export type OwnMerchant = {
  id: string;
  businessName: string;
  status: 'active' | 'suspended';
  settlementMode: SettlementMode | '';
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  chainId?: number;
  tokenAddress?: string;
  tokenSymbol?: string;
  walletAddress?: string;
};

export type SettlementChange =
  | { settlementMode: 'fiat'; bankCode: string; accountNumber: string }
  | { settlementMode: 'crypto'; chainId: number; tokenAddress: string; tokenSymbol: string; walletAddress: string };

/** 422 from a change: the bank account is not in the business's or the owner's verified name. */
export const NAME_MISMATCH = 'name_mismatch';
/** 401 from a change: the account password did not match. */
export const PASSWORD_INCORRECT = 'password_incorrect';

const request = createApiRequest({
  prefix: '/pay/merchant',
  unavailableMessage: 'Payment settings are not available right now. Please try again later.',
  networkMessage: 'Could not reach the server. Check your connection and try again.',
  makeError: (message, status, code) => new PayError(message, status, code),
});

/** Her merchant, or a PayError with status 404 when she has none. */
export function getOwnMerchant(): Promise<OwnMerchant> {
  return request<OwnMerchant>('');
}

/** Change where she is paid. The password is checked again on the server. */
export function updateSettlement(change: SettlementChange, password: string): Promise<OwnMerchant> {
  return request<OwnMerchant>('/settlement', { method: 'PUT', body: JSON.stringify({ ...change, password }) });
}
