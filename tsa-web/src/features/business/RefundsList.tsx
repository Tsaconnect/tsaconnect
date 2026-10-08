import type { ReactNode } from 'react';
import type { Mode } from './api';

export type RefundsListProps = { mode: Mode; from: string; to: string };

/**
 * The period's refunds on the reconciliation page. Refunds don't exist yet, so
 * this renders nothing. The refunds work fills it in, and the page already
 * passes it the period it shows.
 */
export const RefundsList: (props: RefundsListProps) => ReactNode = () => null;
