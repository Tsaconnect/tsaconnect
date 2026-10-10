// services/paycrestDeposit.ts
//
// The sell leg's on-chain half: whether a Paycrest route is spendable from
// this device's wallet, and the send that fulfils it.
//
// This lives outside app/wallet/paycrest/sell.tsx because the answers are
// needed at two different moments — the funding check gates the amount screen
// (before an order and its countdown exist), the send runs on the deposit
// screen — and because neither is testable inside a 500-line component.
import { ethers } from 'ethers';
import { CHAINS, type ChainKey } from '@/constants/chains';
import {
  buildTransferAuthorization,
  getSponsorshipOffer,
  randomAuthorizationNonce,
  relayAuthorization,
  requestGasTopUp,
  type SponsorshipOffer,
} from './gasSponsorship';
import { PaycrestError } from './paycrestApi';
import { signPreparedTransaction } from './signPreparedTransaction';
import { txHashOrThrow, TRANSFER_COPY } from './submitErrors';
import { signTypedData } from './wallet';
import {
  prepareSendTransaction,
  sendPreparedTransaction,
  submitTransaction,
  type WalletBalanceWithChain,
} from './walletApi';

/** A route the user might take, as the quote describes it. */
export interface DepositRoute {
  /** Our chain key — 'base', 'polygon', 'bsc'. */
  chain: string;
  token: string;
  /** What the user must send, in whole tokens. */
  tokenAmount: number;
}

export type FundingCheck =
  | { ok: true }
  | { ok: false; reason: 'unsupported-chain'; chain: string }
  | {
      ok: false;
      reason: 'short-token';
      token: string;
      chain: string;
      have: number;
      need: number;
    }
  | { ok: false; reason: 'no-gas'; nativeSymbol: string; chain: string };

/** A token's balance on one chain; a token the wallet does not hold reads as 0. */
export const balanceOf = (
  balances: WalletBalanceWithChain[],
  chain: string,
  symbol: string,
): number => {
  const row = balances.find(
    (b) => b.chainKey === chain && b.symbol.toUpperCase() === symbol.toUpperCase(),
  );
  const n = Number(row?.balance ?? 0);
  return Number.isFinite(n) ? n : 0;
};

/**
 * Whether the device wallet can actually fund `route`.
 *
 * `balances` is null while the fetch is in flight or after it failed, and that
 * always answers `ok` — a flaky indexer must not lock a user out of a sale
 * they can fund. The real send still fails loudly if the funds aren't there,
 * so the cost of a false pass is the old behaviour, not a worse one.
 *
 * The token shortfall is reported ahead of a gas shortfall: topping up gas
 * would not make the sale possible, so it is the wrong thing to tell the user
 * to go and do first.
 */
export function checkFunding(
  route: DepositRoute,
  balances: WalletBalanceWithChain[] | null,
  opts: { gasSponsored?: boolean } = {},
): FundingCheck {
  const chainCfg = CHAINS[route.chain as ChainKey];
  if (!chainCfg) return { ok: false, reason: 'unsupported-chain', chain: route.chain };

  if (!balances) return { ok: true };

  const have = balanceOf(balances, route.chain, route.token);
  if (have < route.tokenAmount) {
    return {
      ok: false,
      reason: 'short-token',
      token: route.token,
      chain: route.chain,
      have,
      need: route.tokenAmount,
    };
  }

  // Gas is only checked for presence here. What a transfer actually costs is
  // not knowable until the tx is prepared, so the exact comparison happens at
  // send time — this catches the common case (never bridged any gas to this
  // chain) while the user can still pick a different route.
  const nativeSymbol = chainCfg.nativeCurrency.symbol;
  // A sponsored route needs no gas in the wallet: we relay the transfer or
  // top up gas at send time.
  if (!opts.gasSponsored && balanceOf(balances, route.chain, nativeSymbol) <= 0) {
    return { ok: false, reason: 'no-gas', nativeSymbol, chain: route.chain };
  }

  return { ok: true };
}

/** Our chain keys, as a person names them. */
export const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

/** A token amount for display: two decimals from one up, six below — or, for
 *  an amount six decimals would round to zero (L2 gas), two significant digits. */
export const fmtTokenAmount = (n: number): string =>
  n > 0 && n < 0.0000005
    ? n.toLocaleString(undefined, { maximumSignificantDigits: 2 })
    : n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 2 : 6 });

/** The wallet's gas balance on a chain, or null when balances aren't known. */
export function nativeBalanceFor(chain: string, balances: WalletBalanceWithChain[] | null): number | null {
  const symbol = CHAINS[chain as ChainKey]?.nativeCurrency.symbol;
  if (!symbol || !balances) return null;
  const row = balances.find((b) => b.chainKey === chain && b.symbol === symbol);
  const n = Number(row?.balance ?? 0);
  return Number.isFinite(n) ? n : 0;
}

/** A funding verdict as a full sentence for a form, or null when it is fine.
 *  `what` names the thing being funded: 'sale' on the sell screen, 'payment' on TSA Pay. */
export function fundingMessage(check: FundingCheck, what = 'sale'): string | null {
  if (check.ok) return null;
  const chain = CHAIN_LABELS[check.chain] ?? check.chain;
  if (check.reason === 'short-token') {
    return `You have ${check.have} ${check.token} on ${chain} — this ${what} needs ${check.need}. Pick another network or top up.`;
  }
  if (check.reason === 'no-gas') {
    return `You have no ${check.nativeSymbol} on ${chain} to pay the network fee. Pick another network or add some ${check.nativeSymbol}.`;
  }
  return `This app cannot send on ${chain} yet — choose another network, or send from an external wallet.`;
}

/** The parts of a created sell order this send needs. */
export interface DepositOrder {
  chain: string;
  token: string;
  tokenAmount?: string;
  depositAddress?: string;
  /**
   * The exact token and base-units amount, when the caller knows them: the
   * prepared transfer is then built for that contract and checked, before it
   * is signed, to move exactly that amount of it to depositAddress.
   */
  tokenAddress?: string;
  amountBaseUnits?: string;
  decimals?: number;
}

const ERC20_TRANSFER = new ethers.Interface(['function transfer(address to, uint256 amount)']);

/**
 * Throw unless `tx` is an ERC-20 transfer of exactly `amountBaseUnits` of
 * `tokenAddress` to `to` on `chainId`, carrying no native value.
 */
export function assertPreparedTransfer(
  tx: { to: string; data: string; value?: string; chainId: number | string },
  expected: { tokenAddress: string; to: string; amountBaseUnits: string; chainId: number },
): void {
  const wrong = () => new Error('The prepared payment is not the one you were shown, so nothing was sent. Please try again.');
  if (tx.to?.toLowerCase() !== expected.tokenAddress.toLowerCase() || Number(tx.chainId) !== expected.chainId) throw wrong();
  if (tx.value && BigInt(tx.value) !== 0n) throw wrong();
  let args: ethers.Result;
  try {
    args = ERC20_TRANSFER.decodeFunctionData('transfer', tx.data);
  } catch {
    throw wrong();
  }
  if (String(args[0]).toLowerCase() !== expected.to.toLowerCase() || BigInt(args[1]) !== BigInt(expected.amountBaseUnits)) throw wrong();
}

/**
 * A deposit may have been broadcast, but the server's answer never arrived (a
 * timeout, an HTML error page). Its outcome is unknown: sending again could pay
 * twice, so a caller waits on the order rather than offering to send.
 */
export const DEPOSIT_OUTCOME_UNKNOWN =
  "We couldn't confirm whether your payment was sent. It may still arrive — don't send it again yet.";

export class DepositOutcomeUnknownError extends Error {
  constructor(message = DEPOSIT_OUTCOME_UNKNOWN) {
    super(message);
    this.name = 'DepositOutcomeUnknownError';
  }
}

export interface DepositSendOptions {
  /**
   * Runs right before each broadcast, after everything that can refuse the
   * send without sending (gas, prepare, sign) has passed — the moment to
   * record a "this may be out there" safeguard. A throw stops the broadcast.
   */
  beforeBroadcast?: () => Promise<void>;
}

/**
 * Move the stablecoin from the device wallet to the provider's deposit
 * address, fulfilling a sell order.
 *
 * `nativeBalance` is the wallet's gas balance on this chain in whole native
 * units, and is only used to turn an unaffordable gas cost into a sentence the
 * user can act on — the exact cost is not knowable until the tx is prepared.
 * Pass null when balances are unavailable and the send proceeds unguarded.
 *
 * Resolves to the broadcast hash; rejects with a message already mapped for
 * display, which screens can show verbatim — or with DepositOutcomeUnknownError
 * when a broadcast went unanswered. Any other rejection means the server
 * refused every broadcast it was given, so nothing left the wallet.
 */
export async function sendPaycrestDeposit(
  order: DepositOrder,
  fromAddress: string,
  nativeBalance?: number | null,
  opts: DepositSendOptions = {},
): Promise<string> {
  const chainCfg = CHAINS[order.chain as ChainKey];
  if (!chainCfg) {
    throw new Error(`This app cannot send on ${order.chain} yet. Send from another wallet instead.`);
  }
  const to = order.depositAddress?.trim();
  if (!to) throw new Error('This order has no deposit address yet.');
  const amount = order.tokenAmount?.trim();
  if (!amount) throw new Error('This order has no amount to send.');

  const { chainId, nativeCurrency } = chainCfg;

  // Set by any broadcast whose answer never came back — including a retry's.
  let unanswered = false;
  const res = await sendPreparedTransaction({
    prepare: () =>
      prepareSendTransaction(order.token, to, amount, chainId, order.tokenAddress
        ? { fromAddress, tokenAddress: order.tokenAddress, decimals: order.decimals }
        : { fromAddress }),
    sign: (tx) => {
      if (order.tokenAddress && order.amountBaseUnits) {
        assertPreparedTransfer(tx, { tokenAddress: order.tokenAddress, to, amountBaseUnits: order.amountBaseUnits, chainId });
      }
      assertGasAffordable(tx.gasPrice, tx.gasLimit, nativeBalance, nativeCurrency.symbol, order.chain);
      return signPreparedTransaction(tx, fromAddress);
    },
    submit: async (signed) => {
      await opts.beforeBroadcast?.();
      const r = await submitTransaction(signed, 'send', order.token, to, amount, chainId, fromAddress);
      if (r.unanswered) unanswered = true;
      return r;
    },
  });

  if (!(res.success && res.data?.txHash) && unanswered) throw new DepositOutcomeUnknownError();
  // The backend surfaces the RPC's own words here ("max fee per gas less than
  // block base fee: address 0x…, maxFeePerGas: …"). Map before it reaches a
  // screen — every caller of this function shows the message verbatim.
  return txHashOrThrow(res, TRANSFER_COPY);
}

/** A created sell order, with what the sponsored path needs to verify it. */
export interface SponsoredDepositOrder extends DepositOrder {
  id: string;
  walletAddress: string;
}

interface SignedAuthorization {
  nonce: string;
  signature: string;
  validBefore: number;
}

// A signed authorization can be executed by anyone until validBefore, so a
// retry must resubmit the same one — the token contract then makes a double
// spend impossible. An expired one can no longer execute, so minting a new one
// after expiry is safe. Keyed by order id; cleared once a relay resolves.
const signedAuthorizations = new Map<string, SignedAuthorization>();

/** Reuse only with time left for the relay to land before validBefore. */
const AUTHORIZATION_REUSE_MARGIN_MS = 60_000;

/**
 * Send the deposit, letting the backend cover gas where it can.
 *
 * Errors from the relay or top-up propagate rather than falling back to a
 * self-pay send: the user reaching this path typically holds no gas, so the
 * fallback would only replace a clear message with a gas failure.
 */
export async function sendSponsoredPaycrestDeposit(
  order: SponsoredDepositOrder,
  fromAddress: string,
  nativeBalance?: number | null,
  opts: DepositSendOptions = {},
): Promise<string> {
  let offer: SponsorshipOffer | null = null;
  try {
    offer = await getSponsorshipOffer(order.id);
  } catch (err) {
    // Only a 404 means sponsorship is off or the server predates it. Anything
    // else — a 409 after a relay whose response was lost, a 403, a 5xx — must
    // reach the user: self-paying then could send the deposit a second time.
    if (!(err instanceof PaycrestError && err.status === 404)) throw err;
    offer = null;
  }

  if (offer?.mode === 'authorization') {
    const cached = signedAuthorizations.get(order.id);
    const reusable =
      cached && cached.validBefore * 1000 > Date.now() + AUTHORIZATION_REUSE_MARGIN_MS
        ? cached
        : undefined;
    // Built every time, reuse included: the order/offer safety checks inside
    // must pass before anything is relayed.
    const typed = buildTransferAuthorization(offer, order, reusable?.nonce ?? randomAuthorizationNonce());
    let authorization = reusable;
    if (!authorization) {
      // Signed by the order's wallet, not whichever wallet is active: the
      // message's `from` is the order's, and the server recovers against it.
      const signature = await signTypedData(typed.domain, typed.types, typed.message, order.walletAddress);
      authorization = { nonce: typed.message.nonce, signature, validBefore: offer.validBefore as number };
      signedAuthorizations.set(order.id, authorization);
    }
    const res = await relayAuthorization(order.id, {
      validAfter: 0,
      validBefore: authorization.validBefore,
      nonce: authorization.nonce,
      signature: authorization.signature,
    });
    signedAuthorizations.delete(order.id);
    // alreadyUsed: this same authorization already executed — typically a
    // retry whose earlier relay landed but whose response was lost. There is
    // no new hash; the order screen tracks the deposit from here.
    return res.txHash ?? '';
  }

  // A relay needs no beforeBroadcast: the server holds one relay per order and
  // hands a retry the first one's hash. The two self-sent paths below do.
  if (offer?.mode === 'topup') {
    await requestGasTopUp(order.id);
    return sendPaycrestDeposit(order, fromAddress, null, opts);
  }

  return sendPaycrestDeposit(order, fromAddress, nativeBalance, opts);
}

/**
 * Throw before signing when the prepared tx costs more gas than the wallet
 * holds. The RPC would reject it anyway, but as "insufficient funds for gas *
 * price + value" — which reads, wrongly, as if the stablecoin were missing.
 */
function assertGasAffordable(
  gasPrice: string,
  gasLimit: string,
  nativeBalance: number | null | undefined,
  nativeSymbol: string,
  chain: string,
): void {
  if (nativeBalance == null) return;
  let cost: number;
  try {
    cost = Number((BigInt(gasPrice || '0') * BigInt(gasLimit || '0')).toString()) / 1e18;
  } catch {
    return; // unparseable gas figures are the RPC's problem, not a reason to block
  }
  if (cost > 0 && cost > nativeBalance) {
    const label = CHAIN_LABELS[chain] ?? chain;
    throw new Error(
      `Not enough ${nativeSymbol} on ${label} to pay gas — this transfer needs about ${fmtTokenAmount(cost)} ${nativeSymbol} ` +
        `and you have ${fmtTokenAmount(nativeBalance)}. Add a little ${nativeSymbol} on ${label} and try again.`,
    );
  }
}
