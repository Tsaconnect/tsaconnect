import { p2pErrorMessage, p2pKycCode, type P2PErrorLike } from '@/services/p2pErrors';

export interface MarketError {
  kycCode?: string;
  message: string;
}

const FALLBACK = 'Couldn’t load the market. Check your connection.';

/** Maps a failed market fetch to what the screen shows: the KYC state when the
 *  server refused for verification, otherwise a retryable error. */
export function marketLoadError(res: P2PErrorLike): MarketError {
  return { kycCode: p2pKycCode(res), message: p2pErrorMessage(res, FALLBACK) };
}
