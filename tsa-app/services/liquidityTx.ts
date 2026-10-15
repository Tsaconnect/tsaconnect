// services/liquidityTx.ts — the LP's own vault txs (spec 2026-10-14 §5):
// gas check → API prepare → sign as the LP wallet → broadcast → wait mined.
// A deposit whose allowance is short comes back as step "approve": the
// approval is sent and mined, then the deposit is prepared again. An API node
// that has not yet seen the mined approval still answers "approve": that is
// re-asked after a short wait, never signed twice.
//
// Once a tx is broadcast the LP is never told it "failed" unless the chain
// says so: a slow receipt may still land, and the screen re-reads the vault.
import { Interface } from 'ethers';
import { p2pChainKey, p2pNetwork, type ApiResponse } from './p2p';
import { liquidityApi, type LiquidityMe, type LiquidityTxPlan } from './liquidityApi';
import { signAndBroadcast } from './p2pTx';
import { TxRevertedError, waitMined, type MinedReceipt } from './txWait';
import { ensureGas } from './wallet';

export type LiquidityTxKind = 'deposit' | 'withdraw' | 'claim';

/** What a mined withdraw did, in token base units, read from the vault's own events. */
export interface WithdrawalOutcome {
  paidNow: bigint;
  pending: bigint;
}

export type LiquidityTxResult =
  /** `withdrawal`: a withdraw's split, absent when the receipt carried no vault event for it. */
  | { status: 'done'; txHash: string; withdrawal?: WithdrawalOutcome }
  /** Broadcast, but no receipt yet: it may still go through. Do not resend.
   *  `step` says which tx: an approve still needs the deposit after it. */
  | { status: 'pending'; txHash: string; step: LiquidityTxPlan['step'] }
  /** Mined with status 0: nothing changed; safe to try again. */
  | { status: 'reverted'; txHash: string }
  | { status: 'refused'; message: string; code?: string }
  /** No gas on the pool chain; the gas alert has been shown. */
  | { status: 'aborted' }
  | { status: 'busy' };

const MINE_WAIT_MS = 60_000;
/** API node lag after a mined approval: re-prepare this many times, this far apart. */
const APPROVE_LAG_RETRIES = 3;
const APPROVE_LAG_DELAY_MS = 2_000;
export const APPROVAL_CONFIRMING_MESSAGE = 'Your approval is confirming on the network. Try again in a moment.';
const defaultSleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
const inFlight = new Set<string>();

const STAGE: Record<LiquidityTxPlan['step'], string> = {
  approve: 'Approving…',
  deposit: 'Adding liquidity…',
  withdraw: 'Withdrawing…',
  claim: 'Claiming…',
};

const VAULT_EVENTS = new Interface([
  'event Withdrawn(address indexed lp, uint256 amount)',
  'event WithdrawalPending(address indexed lp, uint256 amount)',
]);

/**
 * The paid-now / pending split of a mined withdraw, from the vault's logs in
 * its receipt (never a later API read, which can lag). Only logs emitted by
 * `vault` for `wallet` count. Undefined when neither event is there.
 */
export function decodeWithdrawal(receipt: MinedReceipt | undefined, vault: string, wallet: string): WithdrawalOutcome | undefined {
  let paidNow = 0n;
  let pending = 0n;
  let seen = false;
  for (const log of receipt?.logs ?? []) {
    if (!vault || log.address.toLowerCase() !== vault.toLowerCase()) continue;
    let parsed;
    try {
      parsed = VAULT_EVENTS.parseLog({ topics: [...log.topics], data: log.data });
    } catch {
      continue;
    }
    if (!parsed || String(parsed.args.lp).toLowerCase() !== wallet.toLowerCase()) continue;
    seen = true;
    if (parsed.name === 'Withdrawn') paidNow += parsed.args.amount as bigint;
    else pending += parsed.args.amount as bigint;
  }
  return seen ? { paidNow, pending } : undefined;
}

function prepare(kind: LiquidityTxKind, amount: string): Promise<ApiResponse<LiquidityTxPlan>> {
  if (kind === 'deposit') return liquidityApi.prepareDeposit(amount);
  if (kind === 'withdraw') return liquidityApi.prepareWithdraw(amount);
  return liquidityApi.prepareClaim();
}

export async function runLiquidityTx(p: {
  me: LiquidityMe;
  kind: LiquidityTxKind;
  /** Whole tokens; ignored for a claim. */
  amount?: string;
  wallet: string;
  onStage?: (label: string) => void;
  onGasAlert?: (title: string, message: string) => void;
  /** Injectable for tests. */
  sleep?: (ms: number) => Promise<void>;
}): Promise<LiquidityTxResult> {
  const sleep = p.sleep ?? defaultSleep;
  const chainId = p.me.chain?.id;
  if (!chainId) return { status: 'refused', message: 'The pool network is not available right now.' };
  const key = `${p.kind}:${p.wallet.toLowerCase()}`;
  if (inFlight.has(key)) return { status: 'busy' };
  inFlight.add(key);
  try {
    p.onStage?.('Checking gas…');
    if (!(await ensureGas(p2pChainKey(chainId), p.wallet, p.onGasAlert, p2pNetwork(chainId)))) {
      return { status: 'aborted' };
    }
    let last = '';
    let receipt: MinedReceipt | undefined;
    // An approve, then the deposit: never more than two rounds.
    for (let round = 0; round < 2; round++) {
      p.onStage?.('Preparing…');
      let res = await prepare(p.kind, p.amount ?? '');
      // Round 1 follows a mined approve: "approve" again only means the API's
      // node has not caught up yet. Wait for it; never sign a second approve.
      for (let retry = 0; round > 0 && retry < APPROVE_LAG_RETRIES && res.data?.step === 'approve'; retry++) {
        p.onStage?.('Waiting for the approval to confirm…');
        await sleep(APPROVE_LAG_DELAY_MS);
        res = await prepare(p.kind, p.amount ?? '');
      }
      if (round > 0 && res.success && res.data?.step === 'approve') {
        return { status: 'refused', message: APPROVAL_CONFIRMING_MESSAGE };
      }
      if (!res.success || !res.data) {
        return { status: 'refused', message: res.message || 'Could not prepare the transaction.', code: res.code };
      }
      const plan = res.data;
      if (plan.txs.some((tx) => tx.chainId && Number(tx.chainId) !== chainId)) {
        return { status: 'refused', message: 'This transaction was built for a different network. Pull down to refresh and try again.' };
      }
      for (const tx of plan.txs) {
        const sent = await signAndBroadcast(chainId, tx, p.wallet, { onStage: p.onStage, signing: STAGE[plan.step] });
        last = sent.hash;
        p.onStage?.('Waiting for confirmation…');
        try {
          receipt = await waitMined(chainId, sent.hash, { timeoutMs: MINE_WAIT_MS, provider: sent.provider, label: STAGE[plan.step] });
        } catch (err) {
          if (err instanceof TxRevertedError) return { status: 'reverted', txHash: sent.hash };
          return { status: 'pending', txHash: sent.hash, step: plan.step };
        }
      }
      if (plan.step === 'withdraw') {
        const withdrawal = decodeWithdrawal(receipt, p.me.vault, p.wallet);
        return withdrawal ? { status: 'done', txHash: last, withdrawal } : { status: 'done', txHash: last };
      }
      if (plan.step !== 'approve') return { status: 'done', txHash: last };
    }
    return { status: 'refused', message: APPROVAL_CONFIRMING_MESSAGE };
  } finally {
    inFlight.delete(key);
  }
}
