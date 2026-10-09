import { p2pErrorMessage, type P2PErrorLike } from '../services/p2pErrors';

const KNOWN_409 = /open dispute|invalid state transition/i;

/** Copy for a refused dispute prepare. An uncoded 409 we have no rule for means
 *  the other party's dispute prepare is still live (it holds the trade ~10 min). */
export function disputeRefusalMessage(res: P2PErrorLike): string {
  if (res.status === 409 && !res.code && !KNOWN_409.test(res.message ?? '')) {
    return 'A dispute for this trade is already being prepared. Wait a few minutes, refresh the trade and try again.';
  }
  return p2pErrorMessage(res);
}
