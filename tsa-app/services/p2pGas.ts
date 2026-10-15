// services/p2pGas.ts
//
// Before a P2P tx on a sponsored mainnet (Sonic, BSC), ask the API to drip the
// chain's gas coin (S, BNB) to the account's proven wallet when it can't cover
// `action`, wait for that drip to mine, then let the SIGNING wallet's own balance decide (the drip goes to
// users.wallet_address, which may not be the active HD wallet). Every sponsor
// failure (cap, proof, dormant, in progress) degrades to the plain balance
// check instead of blocking; its refusal copy is shown only if that check fails.
import { Alert } from 'react-native';
import {
  gasApi,
  p2pChainKey,
  p2pNetwork,
  P2P_GAS_SPONSOR_CHAIN_IDS,
  type P2PGasTarget,
  type P2PGasTopUp,
  type P2PGasTopUpRequest,
} from './p2p';
import { ensureGas } from './wallet';
import { waitMined } from './txWait';
import { p2pErrorMessage, p2pKycCode, type P2PErrorLike } from './p2pErrors';
import { promptKycRefusal } from '@/utils/kycRefusal';

const TOPUP_WAIT_MS = 60_000;
const SONIC_MAINNET_CHAIN_ID = 146;

// Refusals that explain why sponsorship did not help. gas_not_needed,
// gas_topup_unused and sponsorship_in_progress describe the PROVEN wallet, which
// may differ from the signer, so for those ensureGas's own message stands. A
// KYC refusal (unverified, or verified with no legal name — the open_trade
// drip's 403) is the real blocker, so it explains it rather than "add S".
function explainsEmptySigner(r: P2PErrorLike): boolean {
  if (r.status === 429) return /used today's gas top-ups/i.test(r.message ?? '');
  return r.code === 'gas_unavailable' || r.code === 'WALLET_PROOF_REQUIRED' || !!p2pKycCode(r);
}

/** The request body: just the target's own fields plus the chain — the params
 *  carry more (runP2PTx passes its whole params object). */
function gasTarget(t: P2PGasTarget, chainId: number): P2PGasTopUpRequest {
  switch (t.action) {
    case 'open_trade':
    case 'cancel_ad':
      return { action: t.action, adId: t.adId, chainId };
    case 'mark_paid':
    case 'release':
    case 'cancel_trade':
    case 'dispute':
      return { action: t.action, tradeId: t.tradeId, chainId };
    default:
      return { action: t.action, chainId };
  }
}

/** The chain a top-up response funded. The API echoes the chainId it was asked
 *  about; an API from before that echo only ever funded Sonic mainnet (146),
 *  and BSC 56 then got S on Sonic — waiting for that hash on BSC stalled the
 *  whole timeout. */
function fundedChainId(drip: P2PGasTopUp): number {
  return drip.chainId ?? SONIC_MAINNET_CHAIN_ID;
}

/** The action's entity id is required by the type (P2PGasTarget). */
export type EnsureP2PGasParams = P2PGasTarget & {
  chainId: number;
  walletAddress: string;
  onStage?: (label: string) => void;
  onAlert?: (title: string, message: string) => void;
};

export async function ensureP2PGas(p: EnsureP2PGasParams): Promise<boolean> {
  const { chainId, walletAddress, onStage, onAlert } = p;
  let refusal: P2PErrorLike | undefined;

  if (P2P_GAS_SPONSOR_CHAIN_IDS.has(chainId)) {
    onStage?.('Checking network fee…');
    try {
      const res = await gasApi.topUp(gasTarget(p, chainId));
      // A 503 gas_unavailable carries the unconfirmed drip's hash: wait on it
      // rather than reporting a failure that may yet land. Only a drip on THIS
      // chain is worth waiting for (fundedChainId); a refusal such as
      // gas_not_needed carries none, so it falls straight through to ensureGas.
      const drip = res.success ? res.data : (res.data as P2PGasTopUp | undefined);
      const hash =
        drip && !drip.skipped && drip.txHash && fundedChainId(drip) === chainId ? drip.txHash : undefined;
      if (!res.success && explainsEmptySigner(res)) refusal = res;
      if (hash) {
        onStage?.('Adding the network fee to your wallet…');
        try {
          await waitMined(chainId, hash, { label: 'Network fee top-up', timeoutMs: TOPUP_WAIT_MS });
          refusal = undefined;
        } catch (err) {
          // A timeout is not a failure: the signer's balance decides below.
          console.warn('[p2pGas] sponsored top-up did not confirm', err);
        }
      }
    } catch (err) {
      console.warn('[p2pGas] top-up request failed', err);
      refusal = { code: 'gas_unavailable' };
    }
  }

  // The existing guard on the signer is the last word. If it still comes up
  // empty and the sponsor refused, say why in plain words — and a KYC refusal
  // is not a gas problem: it gets the KYC prompt (with Verify now).
  const kycCode = p2pKycCode(refusal);
  const alert = (title: string, message: string) => {
    if (kycCode && promptKycRefusal(kycCode, refusal?.message, 'p2p')) return;
    const text = refusal ? p2pErrorMessage(refusal) : message;
    if (onAlert) onAlert(title, text);
    else Alert.alert(title, text);
  };
  return ensureGas(p2pChainKey(chainId), walletAddress, alert, p2pNetwork(chainId));
}
