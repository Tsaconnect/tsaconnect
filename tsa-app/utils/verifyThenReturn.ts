// utils/verifyThenReturn.ts
import type { Href } from 'expo-router';
import { KYC_ROUTE } from '@/utils/kycRefusal';
import { rememberPendingPayment } from '@/utils/postAuthRoute';

/**
 * Sends her to verify her identity, remembering the payment first: KYC can
 * outlast the pay screen (a review takes hours), so finishing it — or the next
 * launch — brings her back to the payment (utils/postAuthRoute.ts).
 */
export async function verifyThenReturnToPayment(
  router: { push(href: Href): void },
  payment: { id: string; expiresAt?: string | null },
): Promise<void> {
  await rememberPendingPayment(payment.id, { expiresAt: payment.expiresAt });
  router.push(`${KYC_ROUTE}?returnTo=pay` as Href);
}
