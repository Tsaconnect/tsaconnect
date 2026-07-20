// services/rewardProgramApi.ts
//
// Client for the Reward PROGRAMME (the two one-time enrolment forms).
//
// Not to be confused with the TP-ladder rewards at /api/rewards/* (see
// rewardsApi in components/services/api.ts) — different feature, different
// endpoints. This one talks to /api/reward-claims.
import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export type RewardClaimKind = 'signup' | 'onboarding';
export type RewardClaimStatus = 'pending' | 'approved' | 'rejected' | 'paid';

// Re-exported from the pure util so screens can import networks and validators
// from either side without this module's AsyncStorage dependency leaking into
// the jest-tested helpers. See utils/rewardForms.ts.
export { PHONE_NETWORKS, type PhoneNetwork } from '@/utils/rewardForms';

export interface RewardClaim {
  id: string;
  kind: RewardClaimKind;
  status: RewardClaimStatus;
  name: string;
  username: string;
  phoneNumber?: string;
  phoneNetwork?: string;
  inviterUsername?: string;
  socialLinks?: string;
  payoutMethod?: string;
  payoutDetails?: string;
  invitedUsernames?: string;
  reviewNotes?: string;
  createdAt?: string;
}

export interface RewardClaimEligibility {
  eligible: boolean;
  reasons: string[];
  verifiedReferrals: number;
  hasTransaction: boolean;
  alreadySubmitted: boolean;
  requiredReferrals: number;
}

export interface RewardFormState {
  claim: RewardClaim | null;
  eligibility: RewardClaimEligibility;
}

export type MyRewardClaims = Record<RewardClaimKind, RewardFormState>;

// No name/username: the server derives identity from the auth token.
export interface SubmitRewardClaimInput {
  kind: RewardClaimKind;
  // signup
  phoneNumber?: string;
  phoneNetwork?: string;
  inviterUsername?: string;
  socialLinks?: string;
  // onboarding
  payoutMethod?: string;
  payoutDetails?: string;
  invitedUsernames?: string;
}

export type ApiResult<T> = { success: boolean; data?: T; message?: string };

async function req<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method,
      headers,
      body: body == null ? undefined : JSON.stringify(body),
    });
    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* empty body */
    }
    if (json?.success) return { success: true, data: json.data as T };
    return { success: false, message: json?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

/** Both forms' submission state + eligibility in one call. */
export const getMyRewardClaims = () => req<MyRewardClaims>('GET', '/reward-claims/me');

export const submitRewardClaim = (input: SubmitRewardClaimInput) =>
  req<RewardClaim>('POST', '/reward-claims', input);
