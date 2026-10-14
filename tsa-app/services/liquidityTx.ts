// services/liquidityTx.ts — the LP's own vault txs (spec 2026-10-14 §5):
// gas check → API prepare → sign as the LP wallet → broadcast → wait mined.
// A deposit whose allowance is short comes back as step "approve": the
// approval is sent and mined, then the deposit is prepared again.
//
// Once a tx is broadcast the LP is never told it "failed" unless the chain
// says so: a slow receipt may still land, and the screen re-reads the vault.
import { p2pChainKey, p2pNetwork, type ApiResponse } from './p2p';
import { liquidityApi, type LiquidityMe, type LiquidityTxPlan } from './liquidityApi';
import { signAndBroadcast } from './p2pTx';
import { TxRevertedError, waitMined } from './txWait';
import { ensureGas } from './wallet';

export type LiquidityTxKind = 'deposit' | 'withdraw' | 'claim';

export type LiquidityTxResult =
  | { status: 'done'; txHash: string }
  /** Broadcast, but no receipt yet: it may still go through. Do not resend. */
  | { status: 'pending'; txHash: string }
  /** Mined with status 0: nothing changed; safe to try again. */
  | { status: 'reverted'; txHash: string }
  | { status: 'refused'; message: string; code?: string }
  /** No gas on the pool chain; the gas alert has been shown. */
  | { status: 'aborted' }
  | { status: 'busy' };

const MINE_WAIT_MS = 60_000;
const inFlight = new Set<string>();

const STAGE: Record<LiquidityTxPlan['step'], string> = {
  approve: 'Approving…',
  deposit: 'Adding liquidity…',
  withdraw: 'Withdrawing…',
  claim: 'Claiming…',
};

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
}): Promise<LiquidityTxResult> {
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
    // An approve, then the deposit: never more than two rounds.
    for (let round = 0; round < 2; round++) {
      p.onStage?.('Preparing…');
      const res = await prepare(p.kind, p.amount ?? '');
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
          await waitMined(chainId, sent.hash, { timeoutMs: MINE_WAIT_MS, provider: sent.provider, label: STAGE[plan.step] });
        } catch (err) {
          if (err instanceof TxRevertedError) return { status: 'reverted', txHash: sent.hash };
          return { status: 'pending', txHash: sent.hash };
        }
      }
      if (plan.step !== 'approve') return { status: 'done', txHash: last };
    }
    return { status: 'refused', message: 'The approval did not take effect. Try again.' };
  } finally {
    inFlight.delete(key);
  }
}
