import { useQuery } from '@tanstack/react-query';
import { ApiError } from '@/lib/api';
import { getMerchant } from './api';

export const MERCHANT_KEY = ['merchant'] as const;

/** The signed-in owner's TSA Pay merchant, shared by every business page. */
export const useMerchant = () => useQuery({ queryKey: MERCHANT_KEY, queryFn: ({ signal }) => getMerchant(signal) });

/**
 * The owner has no merchant: the API's `no_merchant`, or a plain 404 when TSA Pay
 * is switched off and the route is not registered at all.
 */
export const isNoMerchant = (err: unknown) => err instanceof ApiError && err.status === 404 && (err.code === 'no_merchant' || !err.code);
