// services/nameChangePay.ts
//
// Paying a name change's fee from this phone's wallet.
//
// The fee comes from the wallet linked to her account, which the account has
// proven it owns (the wallet-link proof, services/walletProof.ts — the server
// asks for it, and createApiRequest runs it, when it is missing). A stablecoin
// fee is sent from this phone only when this phone holds that wallet.
//
// A stablecoin fee is the wallet's ordinary token transfer (sendPaycrestDeposit
// — the prepare/sign/submit every deposit to an address uses), built for the
// exact token contract and checked, before signing, to move exactly the base
// units she was shown. It is held the way every deposit is (services/payHold):
// recorded right before the broadcast, so an app kill or a lost answer never
// leads to paying twice; the hash is recorded too, so the payment is confirmed
// with it — again and again while the chain catches up — never paid again.
// A hash is forgotten only once the server has used the payment: a change that
// took it only to send it back (paid short, or too late) keeps it on this
// phone, the reference for that refund.
import { CHAINS, getChainByChainId, type ChainKey } from '@/constants/chains';
import { depositOutcomeUnknown } from '@/services/depositGuard';
import {
  checkFunding, fundingMessage, nativeBalanceFor, sendPaycrestDeposit,
} from '@/services/paycrestDeposit';
import { clearHold, holdActive, readHold, writeHold } from '@/services/payHold';
import { readWalletBalances } from '@/services/walletApi';
import {
  NAME_CHANGE_CODES, confirmNameChangePayment,
  type NameChange, type StablecoinOption, type StablecoinTransfer,
} from '@/services/nameChangeApi';
import { errorCode } from '@/utils/payCheckout';

const sendHoldKey = (changeId: string) => `nameChange.payment.${changeId}`;
const txHoldKey = (changeId: string) => `nameChange.paymentTx.${changeId}`;
const refutedKey = (changeId: string) => `nameChange.paymentRefuted.${changeId}`;

const sameAddress = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

/** Our chain key for a chain the server named by key or by id, or undefined when this app cannot send on it. */
export function nameChangeChainKey(chain: string | number | undefined): ChainKey | undefined {
  if (chain === undefined || chain === '') return undefined;
  const key = String(chain).toLowerCase();
  if (key in CHAINS) return key as ChainKey;
  const id = Number(chain);
  return Number.isFinite(id) ? getChainByChainId(id)?.key : undefined;
}

/** The option a coin picked on `chainId` pays with — matched by contract, else by symbol — or undefined. */
export function optionFor(
  options: StablecoinOption[], chainId: number, token?: { symbol?: string; address?: string },
): StablecoinOption | undefined {
  return options.find((o) => o.chainId === chainId && (
    !token
    || (token.address ? sameAddress(o.tokenAddress, token.address) : o.token.toUpperCase() === token.symbol?.toUpperCase())));
}

/** Whether her wallet can pay `option`, as a sentence for the form — null when it can (or can't be known). */
export async function stablecoinShortfall(option: StablecoinOption, wallet: string): Promise<string | null> {
  const chain = getChainByChainId(option.chainId)?.key ?? option.chain;
  const check = checkFunding(
    { chain, token: option.token, tokenAmount: Number(option.amount) },
    await readWalletBalances(wallet),
  );
  return fundingMessage(check, 'payment');
}

/** What a change still to be paid was priced at, for checking its transfer after a restart. */
export function shownFromChange(c: NameChange): StablecoinOption | null {
  if (!c.transfer) return null;
  return {
    chain: c.payChain, chainId: c.transfer.chainId, token: c.payToken, tokenAddress: c.transfer.tokenAddress,
    amount: c.payAmount, amountBaseUnits: c.payAmountBaseUnits, decimals: c.payDecimals,
  };
}

export const PAYMENT_CHANGED =
  'The payment the server asked for is not the one you were shown, so nothing was sent. Go back and try again.';

export const otherWalletMessage = (from: string) =>
  `This fee must be paid from the wallet linked to your account (${from.slice(0, 6)}…${from.slice(-4)}). Switch to that wallet on this phone to pay.`;

/**
 * The transfer is what she agreed to: the token contract, network, decimals
 * and exact base units she was shown, to a named recipient.
 */
function sameAsShown(shown: StablecoinOption, t: StablecoinTransfer): boolean {
  try {
    const amount = BigInt(t.amountBaseUnits);
    return (
      !!t.to?.trim() &&
      t.chainId === shown.chainId &&
      sameAddress(t.tokenAddress, shown.tokenAddress) &&
      t.decimals === shown.decimals &&
      amount > 0n && amount === BigInt(shown.amountBaseUnits)
    );
  } catch {
    return false;
  }
}

/**
 * Send the fee from `wallet` and confirm it. Resolves to the change as the
 * server now has it, or null when the payment left but the chain has not
 * caught up (confirm again later — paymentPending says 'sent'). Rejects with a
 * message fit to show.
 */
export async function payNameChangeInStablecoin(
  changeId: string,
  transfer: StablecoinTransfer,
  shown: StablecoinOption,
  wallet: string,
): Promise<NameChange | null> {
  if (!sameAddress(transfer.from, wallet)) throw new Error(otherWalletMessage(transfer.from));
  if (!sameAsShown(shown, transfer)) throw new Error(PAYMENT_CHANGED);
  const chain = getChainByChainId(transfer.chainId)?.key;
  if (!chain) throw new Error(`This app cannot send on ${transfer.chain} yet.`);

  const balances = await readWalletBalances(wallet);
  const holdKey = sendHoldKey(changeId);
  let txHash: string;
  try {
    txHash = await sendPaycrestDeposit(
      {
        chain, token: transfer.token, tokenAmount: transfer.amount, depositAddress: transfer.to,
        tokenAddress: transfer.tokenAddress, amountBaseUnits: transfer.amountBaseUnits, decimals: transfer.decimals,
      },
      wallet,
      nativeBalanceFor(chain, balances),
      { beforeBroadcast: () => writeHold(holdKey, changeId) },
    );
  } catch (e) {
    // Only a refusal the server answered lifts the hold: nothing left the wallet.
    if (!depositOutcomeUnknown(e)) await clearHold(holdKey);
    throw e;
  }
  await writeHold(txHoldKey(changeId), txHash).catch((e) => console.warn('[name-change] tx hash not recorded', e));
  return confirmSent(changeId, txHash);
}

/**
 * Confirm `txHash`. Not mined yet → null (still confirming, the hold stays).
 * Refuted → recorded as refuted (for support), the holds lifted, and thrown.
 * Taken but unusable (the change comes back cancelled, refunding) → the hash
 * is kept: it is what the refund is about.
 */
async function confirmSent(changeId: string, txHash: string): Promise<NameChange | null> {
  try {
    const change = await confirmNameChangePayment(txHash);
    if (change.status === 'cancelled') await clearHold(sendHoldKey(changeId));
    else await forgetNameChangePayment(changeId);
    return change;
  } catch (e) {
    const code = errorCode(e);
    if (code === NAME_CHANGE_CODES.PAYMENT_PENDING_CHAIN) return null;
    if (code === NAME_CHANGE_CODES.PAYMENT_MISMATCH) {
      await forgetNameChangePayment(changeId);
      await writeHold(refutedKey(changeId), txHash).catch(() => {});
    }
    throw e;
  }
}

export type PaymentPending =
  /** Sent, with a hash the server has not proven yet: confirm again with it. */
  | { kind: 'sent'; txHash: string }
  /** A broadcast may have left without an answer: don't pay again yet. */
  | { kind: 'unknown' }
  /** The server said this transaction can never pay the change: a support case. */
  | { kind: 'refuted'; txHash: string }
  | null;

/** What this phone knows of a payment for `changeId` the server has not accepted. Never throws. */
export async function paymentPending(changeId: string): Promise<PaymentPending> {
  try {
    const tx = await readHold(txHoldKey(changeId));
    if (tx) return { kind: 'sent', txHash: tx.intentId };
    const refuted = await readHold(refutedKey(changeId));
    if (refuted) return { kind: 'refuted', txHash: refuted.intentId };
    return holdActive(await readHold(sendHoldKey(changeId))) ? { kind: 'unknown' } : null;
  } catch {
    // "I could not look" is not "nothing was sent".
    return { kind: 'unknown' };
  }
}

/**
 * Confirm the payment already sent for `changeId` with its recorded hash: the
 * change as the server now has it, or null while the chain catches up.
 */
export async function confirmRecordedPayment(changeId: string): Promise<NameChange | null> {
  const pending = await paymentPending(changeId);
  if (pending?.kind !== 'sent') throw new Error('No payment from this phone is waiting to be confirmed.');
  return confirmSent(changeId, pending.txHash);
}

/** The server has the payment (or never will): nothing here to hold any more. */
export async function forgetNameChangePayment(changeId: string): Promise<void> {
  await Promise.all([clearHold(sendHoldKey(changeId)), clearHold(txHoldKey(changeId))]);
}
