// utils/spotChains.ts
//
// Resolves a Spot listing's `chain` string into everything the Spot Trade desk
// needs to talk to LiFi. The listing stores OUR chain key ("sonic", "solana");
// LiFi addresses every chain — EVM or not — by a numeric id, so the desk works
// in LiFi ids and branches on `family` only where the VM actually differs
// (which address signs, how a balance is read, whether gas is a thing).
//
// Bitcoin is deliberately NOT resolvable here. LiFi lists exactly one token on
// Bitcoin (BTC itself), so no same-chain pair exists — a BTC pair is inherently
// cross-chain, which a single-`chain` listing cannot express and the same-chain
// desk cannot trade. Returning null keeps such a listing from reaching a form
// that could only fail at quote time.
import {
  CHAINS, NONEVM_CHAINS, isNonEVMChain, type ChainKey, type NonEVMChainKey,
} from '@/constants/chains';
import { SOLANA_LIFI_CHAIN_ID, BTC_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID } from '@/services/lifi';

/** VM family of a resolved listing chain — the axis the desk branches on. */
export type SpotChainFamily = 'evm' | 'solana' | 'sui';

export interface SpotChain {
  /** LiFi's numeric id for the chain — quotes, token lists, executor dispatch. */
  lifiChainId: number;
  /** Display name ("Sonic", "Solana"). */
  name: string;
  family: SpotChainFamily;
  /** Set only for EVM, where gas checks and RPC reads need our own chain key. */
  evmKey?: ChainKey;
  /** Set only for non-EVM, for address derivation and explorer links. */
  nonEvmKey?: NonEVMChainKey;
}

/** LiFi ids for the non-EVM chains a same-chain spot pair can exist on. */
const NON_EVM_LIFI_IDS: Partial<Record<NonEVMChainKey, number>> = {
  solana: SOLANA_LIFI_CHAIN_ID,
  sui: SUI_LIFI_CHAIN_ID,
};

/**
 * Maps a listing's chain key to its LiFi chain. Returns null for anything the
 * same-chain desk cannot trade: an unknown key, Tron (LiFi has no Tron route),
 * or Bitcoin (single-asset chain — see the note at the top of this file).
 */
export function resolveSpotChain(chain: string | undefined | null): SpotChain | null {
  const key = (chain || '').trim().toLowerCase();
  if (!key) return null;

  if (isNonEVMChain(key)) {
    const lifiChainId = NON_EVM_LIFI_IDS[key];
    if (lifiChainId === undefined) return null;
    return {
      lifiChainId,
      name: NONEVM_CHAINS[key].name,
      family: key as SpotChainFamily,
      nonEvmKey: key,
    };
  }

  const evm = CHAINS[key as ChainKey];
  if (!evm) return null;
  return { lifiChainId: evm.chainId, name: evm.name, family: 'evm', evmKey: key as ChainKey };
}

/** True when LiFi's id is Bitcoin's — the one chain we reject by design. */
export function isBitcoinSpotChain(chain: string | undefined | null): boolean {
  return (chain || '').trim().toLowerCase() === 'btc';
}

export { BTC_LIFI_CHAIN_ID };
