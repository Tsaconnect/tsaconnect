// services/accountApi.ts
//
// Client for account-lifecycle endpoints. Currently just self-service deletion
// (Apple guideline 5.1.1(v)).

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export interface AccountDeletionResult {
  requestId: string;
  status: string;
  /** ISO timestamp after which the record is permanently erased. */
  purgeAfter: string;
  graceDays: number;
}

export type DeleteAccountResponse =
  | { ok: true; data: AccountDeletionResult }
  | { ok: false; message: string; blockers?: string[] };

/**
 * Closes the signed-in user's account.
 *
 * The backend re-checks the password, refuses (409) while the user has open
 * P2P trades or marketplace orders, and otherwise soft-deletes immediately.
 * On a 409 the response carries `blockers` — a human-readable list of what is
 * still outstanding — so the caller can tell the user what to settle first.
 */
export async function requestAccountDeletion(
  password: string,
  reason?: string,
): Promise<DeleteAccountResponse> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}/users/account/deletion-request`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ password, reason: reason ?? '' }),
    });

    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* non-JSON error body — fall through to the status-code message */
    }

    if (res.ok && json?.success) {
      return { ok: true, data: json.data as AccountDeletionResult };
    }

    return {
      ok: false,
      message: json?.message || `Could not delete your account (${res.status})`,
      blockers: json?.data?.blockers,
    };
  } catch (e: any) {
    return { ok: false, message: e?.message || 'Network error' };
  }
}
