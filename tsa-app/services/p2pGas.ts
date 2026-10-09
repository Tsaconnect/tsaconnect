// services/p2pGas.ts
//
// Before a P2P tx on Sonic mainnet, ask the API to drip S to the account's
// proven wallet when it can't cover `action`, wait for that drip to mine, then
// let the SIGNING wallet's own balance decide (the drip goes to
// users.wallet_address, which may not be the active HD wallet). Every sponsor
// failure (cap, proof, dormant, in progress) degrades to the plain balance
// check instead of blocking; its refusal copy is shown only if that check fails.
import { Alert } from 'react-native';
import { gasApi, p2pChainKey, SONIC_GAS_SPONSOR_CHAIN_IDS, type P2PGasAction } from './p2p';
import { ensureGas } from './wallet';
import { waitMined } from './txWait';
import { KYC_VERIFICATION_REQUIRED } from '@/utils/kycRefusal';
import { p2pErrorMessage, type P2PErrorLike } from './p2pErrors';

const TOPUP_WAIT_MS = 60_000;

// Refusals that explain why sponsorship did not help. gas_not_needed,
// gas_topup_unused and sponsorship_in_progress describe the PROVEN wallet, which
// may differ from the signer, so for those ensureGas's own message stands.
function explainsEmptySigner(r: P2PErrorLike): boolean {
  if (r.status === 429) return /used today's gas top-ups/i.test(r.message ?? '');
  return r.code === 'gas_unavailable' || r.code === 'WALLET_PROOF_REQUIRED' || r.code === KYC_VERIFICATION_REQUIRED;
}

export interface EnsureP2PGasParams {
  chainId: number;
  walletAddress: string;
  action: P2PGasAction;
  /** open_trade / cancel_ad. */
  adId?: string;
  /** mark_paid / release / cancel_trade / dispute. */
  tradeId?: string;
  onStage?: (label: string) => void;
  onAlert?: (title: string, message: string) => void;
}

export async function ensureP2PGas(p: EnsureP2PGasParams): Promise<boolean> {
  const { chainId, walletAddress, action, adId, tradeId, onStage, onAlert } = p;
  let refusal: P2PErrorLike | undefined;

  if (SONIC_GAS_SPONSOR_CHAIN_IDS.has(chainId)) {
    onStage?.('Checking network fee…');
    try {
      const res = await gasApi.topUp({ action, ...(tradeId ? { tradeId } : {}), ...(adId ? { adId } : {}) });
      // A 503 gas_unavailable carries the unconfirmed drip's hash: wait on it
      // rather than reporting a failure that may yet land.
      const hash = res.success
        ? (res.data && !res.data.skipped ? res.data.txHash : undefined)
        : (res.data as { txHash?: string } | undefined)?.txHash;
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
  // empty and the sponsor refused, say why in plain words.
  const alert = (title: string, message: string) => {
    const text = refusal ? p2pErrorMessage(refusal) : message;
    if (onAlert) onAlert(title, text);
    else Alert.alert(title, text);
  };
  return ensureGas(p2pChainKey(chainId), walletAddress, alert);
}
