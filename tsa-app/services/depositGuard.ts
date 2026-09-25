// services/depositGuard.ts
//
// Never send a Paycrest order's deposit twice from this phone.
//
// A deposit she pays gas for herself is broadcast from here, and Paycrest
// reports it only a while after it lands. So on its own, "the order still
// waits for a deposit" does not mean nothing was sent: a broadcast whose answer
// was lost, an app kill after the broadcast, or a claim that failed after a
// good send all leave an order that looks unpaid. What says otherwise is a
// persisted hold (services/payHold), written right before each self-paid
// broadcast and lifted only by a refusal the server answered — or when the
// order is terminal.
//
// Shared by everything that sends a deposit: a TSA Pay naira payment
// (usePayNaira, held under the PAYMENT), a sale's deposit on the sell screen and
// the swapped-sell pipeline (held under the ORDER — depositHoldKey). Callers
// pass the key; the semantics are one copy.
import { SPONSORSHIP_IN_PROGRESS } from './gasSponsorship';
import type { PaycrestOrder } from './paycrestApi';
import {
  DepositOutcomeUnknownError,
  sendSponsoredPaycrestDeposit,
  type SponsoredDepositOrder,
} from './paycrestDeposit';
import { clearHold, holdActive, readHold, writeHold } from './payHold';

/** A send whose deposit may be on-chain although no hash came back: waited on, never sent again. */
export const depositOutcomeUnknown = (e: unknown): boolean =>
  e instanceof DepositOutcomeUnknownError || (e as { code?: unknown } | null)?.code === SPONSORSHIP_IN_PROGRESS;

/**
 * Send `order`'s deposit (the sponsor covers gas where it can), holding it
 * under `holdKey` for each self-paid broadcast. Resolves to the hash — '' for a
 * relay that had already executed.
 *
 * The hold is written right before the broadcast, after everything that can
 * refuse without sending (gas, prepare, sign) has passed, and a hold that
 * cannot be written stops the broadcast. One left under the same key by a
 * DIFFERENT order (a payment's earlier order) covers that order, not this one,
 * so it is replaced. Only a refusal the server answered lifts it — nothing left
 * the wallet. An unknown outcome and a success keep it: the order going
 * terminal lifts it. A relayed deposit needs none (the server holds one relay
 * per order and hands a retry the first one's hash), so it never writes one.
 */
export async function sendHeldDeposit(
  order: SponsoredDepositOrder,
  wallet: string,
  nativeBalance: number | null,
  holdKey: string,
): Promise<string> {
  let held = false;
  try {
    return await sendSponsoredPaycrestDeposit(order, wallet, nativeBalance, {
      beforeBroadcast: async () => {
        const prior = await readHold(holdKey);
        if (prior && prior.intentId !== order.id) await clearHold(holdKey);
        await writeHold(holdKey, order.id);
        held = true;
      },
    });
  } catch (e) {
    if (held && !depositOutcomeUnknown(e)) await clearHold(holdKey);
    throw e;
  }
}

/**
 * Whether `orderId`'s deposit may already have left this phone: the hold under
 * `holdKey` is active and covers it. A hold that cannot be read counts — "I
 * could not look" is not "nothing was sent". Never throws.
 */
export async function depositMayBeOut(holdKey: string, orderId: string): Promise<boolean> {
  try {
    const hold = await readHold(holdKey);
    return !!hold && hold.intentId === orderId && holdActive(hold);
  } catch {
    return true;
  }
}

/** The deposit is still to send: the order waits for it, and nothing says it left. */
export async function stillToSend(
  holdKey: string,
  o: Pick<PaycrestOrder, 'id' | 'status' | 'paymentClaimedAt'>,
): Promise<boolean> {
  return o.status === 'awaiting_deposit' && !o.paymentClaimedAt && !(await depositMayBeOut(holdKey, o.id));
}

/**
 * A resend moves exactly what she was shown: the same chain, token, deposit
 * address and amount. The first send was held to what she saw; a resend is
 * held to the order as she saw it.
 */
export const sameDeposit = (shown: PaycrestOrder, now: PaycrestOrder): boolean =>
  shown.chain?.toLowerCase() === now.chain?.toLowerCase() &&
  shown.token?.toUpperCase() === now.token?.toUpperCase() &&
  !!now.depositAddress && shown.depositAddress?.toLowerCase() === now.depositAddress.toLowerCase() &&
  !!now.tokenAmount && shown.tokenAmount === now.tokenAmount;
