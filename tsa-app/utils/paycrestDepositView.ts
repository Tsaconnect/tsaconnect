// utils/paycrestDepositView.ts
//
// What the sell leg of a Paycrest order should show where the deposit
// instructions go.
//
// The app can send the deposit itself from the wallet on this device, and when
// it does, the transfer is already broadcast before the order screen mounts.
// Rendering "send exactly X to 0x…" at that point is not merely redundant: the
// money has left, and the screen is telling the user to send it again.
//
// Five states, and the difference between the middle three is only what we
// can prove:
//
//   address      nothing sent yet — the user still has to move the money
//   progress     we sent it, and (usually) have the hash to show for it
//   unconfirmed  this phone may have sent it but has no hash to show (its hold
//                is active, or the send ended with an unknown outcome) — never
//                asked for again, and no address to send it to twice
//   claimed      the user says it is sent, but this screen cannot see the hash —
//                a restart lost it, or they sent from another wallet entirely
//   hidden       the window closed, or the order moved past waiting on the user
//
// `claimed` keeps the address reachable behind a disclosure rather than
// dropping it, because "I've sent it" is a button someone can press before
// they actually have.
//
// Pure, so every state is testable without a router, a poll or a wallet.
import type { PaycrestOrder } from '@/services/paycrestApi';
import { isAwaitingUser, type PaycrestStatus } from '@/services/paycrestStatus';

export interface DepositViewInput {
  direction: PaycrestOrder['direction'];
  status: PaycrestStatus;
  /** The local countdown has run out. Never send after this. */
  expired: boolean;
  /** Set while this app is broadcasting the deposit right now. */
  sending?: boolean;
  /** Hash of the deposit this app sent, once it is broadcast. */
  sentTxHash?: string;
  /** Server-side record that the user asserted they sent it. */
  paymentClaimedAt?: string;
  /**
   * This phone may have sent it, with no hash to show: its hold is active
   * (a lost answer, a kill after the broadcast, a failed claim) or the send
   * that brought her here ended with an unknown outcome.
   */
  mayBeOut?: boolean;
}

export type DepositCardState =
  | { kind: 'address' }
  | { kind: 'progress'; txHash?: string }
  | { kind: 'unconfirmed' }
  | { kind: 'claimed' }
  | { kind: 'hidden' };

export function depositCardState(input: DepositViewInput): DepositCardState {
  const { direction, status, expired, sending, sentTxHash, paymentClaimedAt, mayBeOut } = input;

  // The buy leg pays into a bank account; it has no deposit address at all.
  if (direction !== 'sell') return { kind: 'hidden' };
  if (!isAwaitingUser(status)) return { kind: 'hidden' };
  // Expiry wins over everything, including a send we made: after the window
  // closes the address is actively dangerous, and a late hash is not progress.
  if (expired) return { kind: 'hidden' };

  if (sentTxHash) return { kind: 'progress', txHash: sentTxHash };
  if (sending) return { kind: 'progress' };
  // Ahead of `claimed`: that card keeps the address one tap away, and money
  // that may have left THIS phone must not be offered a second destination.
  if (mayBeOut) return { kind: 'unconfirmed' };
  if (paymentClaimedAt) return { kind: 'claimed' };

  return { kind: 'address' };
}
