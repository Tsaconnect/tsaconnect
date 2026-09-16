// services/disputesApi.ts
//
// Client for /api/disputes — one complaint queue covering ramp orders, instant
// trades and bill purchases. P2P trades and marketplace orders are deliberately
// absent: resolving those moves funds through their own machinery (escrow
// adminResolve, stock restore), so they keep their own flows.
//
// A dispute is filed with US, not with the payment provider. We escalate to
// Paycrest or Flutterwave where that is what it takes, and the `escalated`
// status is how the user sees that happening.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

/** Order kinds that can be disputed. The strings are a wire contract. */
export type DisputeSubjectType = 'paycrest_order' | 'it_intent' | 'bill_order';

export type DisputeStatus = 'open' | 'escalated' | 'resolved' | 'rejected';

export interface Dispute {
  id: string;
  subjectType: DisputeSubjectType;
  subjectId: string;
  userId: string;
  reason: string;
  evidenceUrls?: string[];
  status: DisputeStatus;
  /** The provider's own handle on the escalation, when there is one. */
  externalRef?: string;
  escalatedAt?: string;
  resolution?: string;
  resolvedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface FileDisputeInput {
  subjectType: DisputeSubjectType;
  subjectId: string;
  reason: string;
  evidenceUrls?: string[];
}

interface ApiResult<T> {
  success: boolean;
  data?: T;
  message?: string;
}

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
      /* empty */
    }
    if (json?.success) return { success: true, data: json.data as T };
    return { success: false, message: json?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

/** Open a dispute on one of your own orders. One per order. */
export const fileDispute = (input: FileDisputeInput) =>
  req<Dispute>('POST', '/disputes', input);

/** Your disputes, newest first. */
export const getDisputes = () =>
  req<{ disputes: Dispute[] }>('GET', '/disputes');

export const getDispute = (id: string) => req<Dispute>('GET', `/disputes/${id}`);

/**
 * The dispute you have already filed on this order, or null if there is none.
 *
 * "None" is the ordinary case, not a failure — every undisputed order in the
 * app asks this question — so a 404 resolves to null and only a real error
 * reports one. Mapping a 500 to null instead would re-offer the form and let a
 * second dispute be filed on the same order.
 */
export async function getDisputeForSubject(
  subjectType: DisputeSubjectType,
  subjectId: string,
): Promise<ApiResult<Dispute | null>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(
      `${API_BASE_URL}/disputes/subject/${subjectType}/${encodeURIComponent(subjectId)}`,
      { method: 'GET', headers },
    );
    if (res.status === 404) return { success: true, data: null };
    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* empty */
    }
    if (json?.success) return { success: true, data: json.data as Dispute };
    return { success: false, message: json?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

/** How the subject lookup ended. */
export type DisputeLookup = 'pending' | 'found' | 'none' | 'failed';

/** What an order screen should render for its dispute affordance. */
export type DisputeAffordance = 'hidden' | 'offer' | 'show-dispute';

/**
 * Decide what to show, given how the lookup went.
 *
 * A FAILED lookup still offers the form. Hiding it was the safer-looking
 * choice and the wrong one: one dropped request left the user with no way to
 * report a problem and nothing on screen saying why. The real guard against a
 * duplicate is the server's unique index, which refuses with a message the
 * form shows — so a failed lookup costs a clear error at worst, where hiding
 * costs the user the feature.
 */
export function disputeAffordance(input: {
  lookup: DisputeLookup;
  disputable: boolean;
}): DisputeAffordance {
  if (input.lookup === 'found') return 'show-dispute';
  if (input.lookup === 'pending') return 'hidden';
  return input.disputable ? 'offer' : 'hidden';
}

/** Whether the user is still waiting on an answer. */
export const isDisputeOpen = (status: DisputeStatus): boolean =>
  status === 'open' || status === 'escalated';

/** Status in the user's terms rather than ours. */
export function disputeStatusLabel(status: DisputeStatus): string {
  switch (status) {
    case 'open':
      return 'Under review';
    case 'escalated':
      // Says what is actually happening: it left our desk and is with whoever
      // holds the money. "Escalated" alone reads like a complaint about us.
      return 'With the payment provider';
    case 'resolved':
      return 'Resolved';
    case 'rejected':
      // Not "Rejected": the resolution text carries the reason, and the label
      // should not be the harshest word on the screen.
      return 'Closed';
  }
}
