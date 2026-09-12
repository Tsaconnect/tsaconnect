// utils/spotChains.ts
//
// Resolves a Spot listing's `chain` string into everything the Spot Trade desk
// needs to talk to LiFi, plus the desk's pure per-family rules. The listing
// stores OUR chain key ("sonic", "solana"); LiFi addresses every chain — EVM or
// not — by a numeric id, so the desk works in LiFi ids and branches on `family`
// only where the VM actually differs (which address signs, how a balance is
// read, whether gas is a thing).
//
// The rules live here rather than in the screen because they are the part worth
// testing: a balance matcher that decides which of the wallet's rows IS the
// user's leg cannot be trusted on inspection alone, and nothing in a .tsx
// screen can be imported by a test.
//
// Bitcoin is deliberately NOT resolvable here. LiFi lists exactly one token on
// Bitcoin (BTC itself), so no same-chain pair exists — a BTC pair is inherently
// cross-chain, which a single-`chain` listing cannot express and the same-chain
// desk cannot trade. Returning null keeps such a listing from reaching a form
// that could only fail at quote time.
import {
  CHAINS, CHAIN_KEYS, NONEVM_CHAINS, isNonEVMChain, type ChainKey, type NonEVMChainKey,
} from '@/constants/chains';
import { SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID, SOLANA_NATIVE_ADDRESS, type LiFiToken } from '@/services/lifi';
import type { WalletBalanceWithChain } from '@/services/walletApi';

/** VM family of a resolved listing chain — the axis the desk branches on. */
export type SpotChainFamily = 'evm' | 'solana' | 'sui';
/** The families whose balances come from the wallet's rows, not from an RPC. */
export type NonEvmSpotChainFamily = Exclude<SpotChainFamily, 'evm'>;

interface SpotChainBase {
  /** LiFi's numeric id for the chain — quotes, token lists, executor dispatch. */
  lifiChainId: number;
  /** Display name ("Sonic", "Solana"). */
  name: string;
}

export interface EvmSpotChain extends SpotChainBase {
  family: 'evm';
  /** Required, not optional: gas checks and RPC reads need our own chain key,
   *  and an EVM chain resolved without one cannot exist. Callers that had to
   *  re-check it were guarding against a state the resolver never produces. */
  evmKey: ChainKey;
}

export interface NonEvmSpotChain extends SpotChainBase {
  family: NonEvmSpotChainFamily;
  /** Required on this variant, for address derivation and explorer links. */
  nonEvmKey: NonEVMChainKey;
}

/** Discriminated so every branch on `family` narrows to the keys that family
 *  actually carries — an EVM chain cannot be read as a Solana one, or back. */
export type SpotChain = EvmSpotChain | NonEvmSpotChain;

/** LiFi ids for the non-EVM chains a same-chain spot pair can exist on. Typed
 *  as a total record: adding a VM family without giving it an id is a type
 *  error here, which is how the family stays impossible to half-support. */
const NON_EVM_LIFI_IDS: Record<NonEvmSpotChainFamily, number> = {
  solana: SOLANA_LIFI_CHAIN_ID,
  sui: SUI_LIFI_CHAIN_ID,
};

const isTradableNonEvm = (key: NonEVMChainKey): key is NonEvmSpotChainFamily =>
  Object.prototype.hasOwnProperty.call(NON_EVM_LIFI_IDS, key);

/**
 * Compile-time exhaustiveness guard for a branch on VM family.
 *
 * Without it a new family falls into whichever branch happens to be last, and
 * inherits rules written for a different VM — silently, at runtime, on the
 * money path. Reaching it at runtime means a family arrived from outside the
 * type system, so it throws rather than guessing.
 */
export function unhandledFamily(value: never): never {
  throw new Error(`Unhandled spot chain family: ${JSON.stringify(value)}`);
}

/**
 * The single value of a route param.
 *
 * expo-router rebuilds its params object on every render, and a REPEATED query
 * param (`?chain=a&chain=b`) arrives as an array that is rebuilt with it — a
 * fresh identity every render even though the URL never changed. A scalar
 * compares equal and settles; an array never does, so every memo and effect
 * keyed on the raw value re-runs forever and the screen spins instead of
 * rendering. Collapsing to a string once, at the edge, is what makes the value
 * safe to depend on.
 *
 * A repeated param is ambiguous, so it collapses to '' rather than to a guess
 * at which copy the caller meant; the desk then says the network is unsupported.
 */
export function singleParam(value: string | string[] | undefined): string {
  return typeof value === 'string' ? value : '';
}

/**
 * Maps a listing's chain key to its LiFi chain. Returns null for anything the
 * same-chain desk cannot trade: an unknown key, Tron (LiFi has no Tron route),
 * or Bitcoin (single-asset chain — see the note at the top of this file).
 *
 * The key is untrusted — it comes from the API and from the screen's own route
 * params. Anything that is not a recognised key resolves to null.
 */
export function resolveSpotChain(chain: unknown): SpotChain | null {
  if (typeof chain !== 'string') return null;
  const key = chain.trim().toLowerCase();
  if (!key) return null;

  if (isNonEVMChain(key)) {
    if (!isTradableNonEvm(key)) return null;
    return {
      lifiChainId: NON_EVM_LIFI_IDS[key],
      name: NONEVM_CHAINS[key].name,
      family: key,
      nonEvmKey: key,
    };
  }

  // Membership is checked against the key LIST, not by indexing CHAINS: a plain
  // object resolves inherited members, so CHAINS['constructor'] is truthy and
  // would sail through as a chain named "Object".
  if (!(CHAIN_KEYS as string[]).includes(key)) return null;
  const evm = CHAINS[key as ChainKey];
  return { lifiChainId: evm.chainId, name: evm.name, family: 'evm', evmKey: key as ChainKey };
}

/** True when the key is Bitcoin's — the one chain we reject by design. */
export function isBitcoinSpotChain(chain: unknown): boolean {
  return typeof chain === 'string' && chain.trim().toLowerCase() === 'btc';
}

// Bitcoin gets its own wording: it is not a gap in the user's wallet, it is
// that a single-asset chain has no same-chain pair for this desk to trade.
export function unsupportedChainMessage(chain: unknown): string {
  return isBitcoinSpotChain(chain)
    ? 'Bitcoin pairs have to route across chains, which this desk doesn’t do yet. Use All Swap to trade BTC.'
    : 'This pair’s network is not supported on this device.';
}

/** The balance-row fields the matcher reads — a wallet row satisfies it. */
export type SpotBalanceRow = Pick<WalletBalanceWithChain, 'symbol' | 'balance' | 'contractAddress'>;
/** The leg fields the matcher reads — a LiFi token satisfies it. */
export type SpotTokenLeg = Pick<LiFiToken, 'symbol' | 'address'>;

/**
 * Balance of one leg, picked out of the wallet's rows for this chain. A token
 * the wallet does not hold reads as zero.
 *
 * Neither family may identify a leg by symbol while an on-chain id is
 * available. Anyone can publish a Sui coin or a Solana mint whose symbol is
 * "USDC", and a symbol match would report the impostor's balance as the real
 * leg's: the user is shown funds they do not have and Max sizes a trade against
 * them. The quote is always built from LiFi's own address, so this cannot
 * mis-send money — it fails at execution instead, after the user committed.
 */
export function matchSpotBalance(
  family: NonEvmSpotChainFamily, rows: SpotBalanceRow[], token: SpotTokenLeg,
): string {
  switch (family) {
    case 'solana': {
      // LiFi gives native SOL a placeholder address and the wallet's native row
      // carries no mint at all, so those two only line up by their absence.
      const isNative = token.address === SOLANA_NATIVE_ADDRESS;
      const row = rows.find((r) => (isNative ? !r.contractAddress : r.contractAddress === token.address));
      return row?.balance ?? '0';
    }
    case 'sui': {
      // Coin type decides it whenever the row carries one. Symbol is the
      // fallback only for the native SUI row, which is the one row a wallet
      // reports with no coin type — and the one row an impostor cannot forge.
      // Hex case is the only normalisation: a coin type's package address may
      // come back in either case, while two struct tags that differ in case
      // belong to the same package and so to the same publisher.
      const wanted = token.address.trim().toLowerCase();
      const row = rows.find((r) => (r.contractAddress
        ? r.contractAddress.trim().toLowerCase() === wanted
        : r.symbol.toUpperCase() === token.symbol.toUpperCase()));
      return row?.balance ?? '0';
    }
    default:
      return unhandledFamily(family);
  }
}
