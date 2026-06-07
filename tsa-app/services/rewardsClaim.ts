// services/rewardsClaim.ts
//
// SP8 reward-claim broadcast helper.
//
// The backend prepares the unsigned claim(account, cumulative, proof) tx but —
// unlike the P2P prepare endpoints — only fills {to, value, data, chainId}. It
// deliberately leaves nonce + gas off (the distributor address/chainId are the
// only chain-specific bits it knows). So this helper completes the tx
// client-side against the live RPC (nonce, gasPrice, gasLimit), then reuses the
// SAME signTransaction → broadcastTransaction path every other on-chain action
// in the app uses. The backend never signs.
//
// Flow mirrors P2P pool.tsx: ensureGas → fill → sign → broadcast → wait.

import { ethers } from 'ethers';
import { getChainByChainId, type ChainKey } from '../constants/chains';
import type { RewardUnsignedTx } from '../components/services/api';
import {
  broadcastTransaction,
  ensureGas,
  getActiveWallet,
  getProvider,
  signTransaction,
} from './wallet';

export interface ClaimBroadcastResult {
  txHash: string;
  /** ChainKey the tx was broadcast on (for explorer links). */
  chainKey: ChainKey;
}

/** Raised when the wallet that controls the registered claim `account` is not the
 *  app's active wallet. The MCGP is paid to `account` regardless of who signs,
 *  but gas + signing must come from `account`'s key — so the user has to switch
 *  to (or import) that wallet first. The caller surfaces this distinctly. */
export class WrongWalletError extends Error {
  constructor(
    public readonly registered: string,
    public readonly active: string | null,
  ) {
    super(
      `This reward pays your registered wallet ${registered}. ` +
        (active
          ? `Your active wallet is ${active}. Switch to ${registered} to claim.`
          : `Set up or switch to ${registered} to claim.`),
    );
    this.name = 'WrongWalletError';
  }
}

/** Raised when the claim tx is guaranteed to revert (estimateGas reverted). The
 *  on-chain claim() reverts "MCGP: nothing to claim" once fully claimed; we must
 *  NOT fall back to a fixed gas limit and broadcast a known-revert tx (the user
 *  would pay gas for nothing). The caller maps this to "nothing to claim". */
export class ClaimWouldRevertError extends Error {
  constructor(message?: string) {
    super(message || 'This claim would revert on-chain (nothing left to claim).');
    this.name = 'ClaimWouldRevertError';
  }
}

/** Resolve the ChainKey for the distributor's chainId. Throws when the chain
 *  isn't one the app knows about (mis-config) rather than silently sending to
 *  the wrong network. */
function chainKeyForId(chainId: number): ChainKey {
  const chain = getChainByChainId(chainId);
  if (!chain) {
    throw new Error(
      `Reward distributor is on an unsupported chain (id ${chainId}). Update the app to claim.`,
    );
  }
  return chain.key;
}

/**
 * Complete, sign, and broadcast the prepared claim tx. Returns the broadcast
 * hash. Throws on any failure — the caller maps these to UX:
 *   - WrongWalletError      the active wallet isn't the registered claim account
 *   - ClaimWouldRevertError estimateGas reverted ⇒ nothing to claim (no broadcast)
 *   - other Error           no wallet, RPC, or chain rejection
 *
 * The claim's MCGP is paid to the backend-registered `account` (the leaf is bound
 * to it) REGARDLESS of msg.sender, but gas + the EIP-155 signature must come from
 * `account`'s key. So we resolve gas/nonce/signing from `account` (not the active
 * wallet) and pin signing to it — catching a wallet mismatch up-front instead of
 * gas-guarding the wrong balance and showing misleading "on its way" copy.
 *
 * @param prepared  The backend's unsigned claim tx ({to,value,data,chainId}).
 * @param account   The registered wallet the claim pays + must be signed by
 *                  (claimData.account). When omitted, falls back to the active
 *                  wallet (legacy behavior) — callers should always pass it.
 * @param onStage   Optional progress callback for the calling screen's button
 *                  label ('Checking gas…', 'Signing…', 'Broadcasting…').
 * @param onGasShort  Called (title, message) when the wallet has no gas, so the
 *                    screen can surface it. When omitted, ensureGas falls back
 *                    to a native Alert. Returns without broadcasting.
 */
export async function broadcastRewardClaim(
  prepared: RewardUnsignedTx,
  account?: string,
  onStage?: (stage: string) => void,
  onGasShort?: (title: string, message: string) => void,
): Promise<ClaimBroadcastResult | null> {
  const chainId = Number(prepared.chainId);
  if (!Number.isFinite(chainId) || chainId <= 0) {
    throw new Error('Prepared claim has an invalid chainId.');
  }
  const chainKey = chainKeyForId(chainId);

  const active = await getActiveWallet();

  // The wallet that pays gas + signs MUST be the registered claim account (the
  // leaf is bound to it). If the app doesn't control that wallet — i.e. the
  // active wallet differs (multi-wallet is switchable) — stop up-front with a
  // clear "switch wallet" error rather than gas-guarding/signing the wrong key.
  const registered = account && account.trim() ? account.trim() : null;
  const wallet = registered ?? active;
  if (!wallet) {
    throw new Error('No active wallet. Set one up from the wallet screen first.');
  }
  if (registered && (!active || active.toLowerCase() !== registered.toLowerCase())) {
    throw new WrongWalletError(registered, active);
  }

  // Gas guard — the claim tx pays gas on the distributor's chain. Bail with a
  // fund-your-wallet prompt rather than letting the tx fail mid-flow. Checks the
  // REGISTERED wallet's balance (the one that actually pays), not the active one.
  onStage?.('Checking gas…');
  const hasGas = await ensureGas(chainKey, wallet, onGasShort);
  if (!hasGas) return null;

  onStage?.('Preparing transaction…');
  const provider = getProvider(chainKey);
  const data = prepared.data.startsWith('0x') ? prepared.data : `0x${prepared.data}`;

  // Fill the chain-specific fields the backend omitted. Each read degrades to a
  // sane default so a single flaky RPC field doesn't brick the claim.
  const [nonce, feeData] = await Promise.all([
    provider.getTransactionCount(wallet, 'pending'),
    provider.getFeeData().catch(() => null),
  ]);

  // Legacy (type-0) gas price — matches signTransaction's legacy tx build.
  const gasPrice = feeData?.gasPrice ?? ethers.parseUnits('1', 'gwei');

  // Estimate gas with a 25% headroom. A revert here is a HARD STOP: the claim()
  // reverts "MCGP: nothing to claim" once fully claimed, so falling back to a
  // fixed 300k limit and broadcasting would make the user pay gas for a
  // guaranteed-revert tx. Surface it as ClaimWouldRevertError instead.
  let gasLimit: bigint;
  try {
    const est = await provider.estimateGas({
      from: wallet,
      to: prepared.to,
      value: prepared.value || '0x0',
      data,
    });
    gasLimit = (est * 125n) / 100n;
  } catch (err: any) {
    throw new ClaimWouldRevertError(
      typeof err?.shortMessage === 'string' ? err.shortMessage : undefined,
    );
  }

  onStage?.('Signing…');
  const signed = await signTransaction(
    {
      to: prepared.to,
      value: prepared.value || '0x0',
      data,
      nonce,
      gasLimit,
      gasPrice,
      chainId,
    },
    // Pin signing to the registered wallet so a mismatch (wrong stored key) is
    // caught here, not by the chain rejecting a wrong-signer tx.
    wallet,
  );

  onStage?.('Broadcasting…');
  const resp = await broadcastTransaction(chainKey, signed);

  // Wait for one confirmation so the post-claim summary refresh reflects the
  // new on-chain claimed amount. Non-fatal if the wait times out — the hash is
  // already on-chain and the reconcile/refresh will catch up.
  onStage?.('Confirming…');
  try {
    await resp.wait(1);
  } catch (err) {
    console.warn('[rewardsClaim] confirmation wait failed (tx already broadcast):', err);
  }

  return { txHash: resp.hash, chainKey };
}
