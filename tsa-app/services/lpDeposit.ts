// services/lpDeposit.ts — the TSA Liquidity "Add liquidity" submit (API spec
// §8.3–§8.4): fresh balance → exact funding → stock ad.
//
// Money rule: the amount deposited is EXACTLY what the LP typed. The funding
// target is the FRESH on-chain `available` (read now, uncached) plus that
// amount — never the 30 s-cached /liquidity/me value (review I4) — and
// ensureEscrowFunding is pinned to that exact deposit, so a balance that moves
// between the two reads stops the deposit instead of widening it. The LP max
// and first-deposit minimum are re-checked on the fresh read; the API checks
// them again on its own fresh read when it prepares the deposit.
import { formatTokenAmount, parseTokenAmount, poolApi } from './p2p';
import { ensureEscrowFunding } from './escrowFunding';
import { publishActionForFunding } from './createAdFunding';
import { createAndSignAd } from './adPublish';
import { P2PCopyError, p2pCaughtMessage } from './p2pErrors';
import type { LiquidityMe } from './liquidityApi';
import { lpDepositPlan } from '../utils/liquidityView';

export type LpDepositResult =
  | { status: 'invalid'; error: string }
  | { status: 'aborted' }
  | { status: 'recover' }
  | { status: 'done'; depositedUnits: string; stockAd: boolean };

const NOTHING_DEPOSITED = 'Nothing was deposited — your balance changed while we checked. Try again.';

/** '' (renew only) or a plain decimal with no more fraction digits than the token has. */
function inputError(input: string, decimals: number): string | null {
  const v = input.trim();
  if (v === '') return null;
  const m = /^\d+(?:\.(\d+))?$/.exec(v);
  if (!m) return 'Enter a valid amount.';
  if ((m[1]?.length ?? 0) > decimals) return `Use at most ${decimals} decimal places.`;
  return null;
}

export async function runLpDeposit(p: {
  me: LiquidityMe;
  input: string;
  wallet: string;
  onStage?: (s: string) => void;
  onGasAlert?: (m: string) => void;
  statusError: (status?: number, message?: string) => string;
}): Promise<LpDepositResult> {
  const { me, wallet } = p;
  if (me.token) {
    const err = inputError(p.input, me.token.decimals);
    if (err) return { status: 'invalid', error: err };
  }
  const plan = lpDepositPlan(me, p.input);
  if (!plan.ok) return { status: 'invalid', error: plan.error };
  if (!me.chain) return { status: 'invalid', error: 'The pool network is not available right now.' };
  const token = me.token!;
  const chainId = me.chain.id;
  const d = token.decimals;
  const sym = token.symbol;

  let deposited = '0';
  if (plan.amountBaseUnits !== '0') {
    const amount = BigInt(plan.amountBaseUnits);
    p.onStage?.('Checking balance…');
    const fresh = await poolApi.balance(sym, wallet, chainId);
    if (!fresh.success || !fresh.data) {
      return { status: 'invalid', error: "We couldn't read your pool balance. Nothing was deposited; try again." };
    }
    const available = BigInt(fresh.data.available);
    const held = available + BigInt(fresh.data.locked);
    const max = BigInt(parseTokenAmount(me.limits.max, d));
    if (held + amount > max) {
      const room = max > held ? max - held : 0n;
      return { status: 'invalid', error: `You can add at most ${formatTokenAmount(room.toString(), d, 4)} ${sym} (limit ${me.limits.max} ${sym}).` };
    }
    if (held === 0n && amount < BigInt(parseTokenAmount(me.limits.min, d))) {
      return { status: 'invalid', error: `Your first deposit must be at least ${me.limits.min} ${sym}.` };
    }

    const fund = await ensureEscrowFunding({
      token: sym,
      chainId,
      amountBaseUnits: (available + amount).toString(),
      exactDepositBaseUnits: plan.amountBaseUnits,
      walletAddress: wallet,
      onStage: p.onStage,
      onGasAlert: p.onGasAlert,
    });
    // Either way nothing was sent: the balance moved between the reads.
    if (fund.status === 'already-funded') return { status: 'invalid', error: NOTHING_DEPOSITED };
    if (fund.status === 'aborted' && fund.reason === 'balance-changed') return { status: 'invalid', error: NOTHING_DEPOSITED };
    const action = publishActionForFunding(fund);
    if (action.kind === 'abort') return { status: 'aborted' };
    if (action.kind === 'recover') return { status: 'recover' };
    deposited = fund.status === 'funded' ? fund.deposited : '0';
  }

  if (plan.needsStockAd) {
    try {
      // tokenAmount '1' satisfies the request binding; the API sizes the
      // voucher itself (plan B P8). No rate, no payment methods: the pool
      // prices and settles orders.
      await createAndSignAd({
        payload: { token: sym, tokenAmount: '1', fiatCurrency: 'NGN', paymentMethodIds: [], sellerAddress: wallet, chainId },
        sellerAddress: wallet,
        onStage: p.onStage,
        statusError: p.statusError,
      });
    } catch (err) {
      if (deposited === '0') throw err;
      // The deposit landed; only the pool ad failed. Say so, so the LP retries
      // the ad (Renew, empty amount) instead of depositing again.
      throw new P2PCopyError(
        `Your ${formatTokenAmount(deposited, d, 4)} ${sym} is safe in your escrow balance, but it could not ` +
          `be added to the pool yet: ${p2pCaughtMessage(err)} Leave the amount empty and tap Renew to retry.`,
      );
    }
  }
  return { status: 'done', depositedUnits: deposited, stockAd: plan.needsStockAd };
}
