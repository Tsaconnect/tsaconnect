// services/assetCatalog.ts
//
// Pure logic behind the asset picker: turn LiFi's per-chain token rows into
// ONE entry per coin with its networks, attach what the wallet holds, and order
// everything the way the picker shows it. No fetching here — see
// hooks/useAssetCatalog — so every rule is unit-testable.
//
// Grouping is by LiFi's coinKey (the same id for USDC on Base and on BSC), never
// by symbol: anyone can deploy a token called "USDC", and matching on the name
// is how a lookalike would end up listed as a network of the real coin.

import {
  type LiFiToken,
  isNativeTokenAddress,
  isSignableSourceChain,
  isBtcLiFiChain,
  isSolanaLiFiChain,
  isSuiLiFiChain,
  SOLANA_NATIVE_ADDRESS,
} from './lifi';

/** LiFi's address for each non-EVM family's native coin (EVM uses 0x000…0). */
const BTC_NATIVE_ADDRESS = 'bitcoin';
const SUI_NATIVE_ADDRESS = '0x2::sui::SUI';
const EVM_NATIVE_ADDRESS = '0x0000000000000000000000000000000000000000';

/** Coins in the default "Stablecoins & major coins" section, in this order. */
export const MAJOR_COIN_KEYS = ['USDC', 'USDT', 'ETH', 'BNB', 'SOL', 'BTC', 'SUI'] as const;

/** Coins never offered, even from search (product decision: TRX is hidden). */
export const HIDDEN_COIN_KEYS = new Set(['TRX']);

/** Preferred network when the wallet holds a coin nowhere: Base, then these. */
const NETWORK_PREFERENCE = [8453, 56, 42161, 10, 137, 146, 1];

export type PickerSide = 'from' | 'to';

/** A wallet balance for one token on one chain. */
export interface Holding {
  chainId: number;
  /** '' for the chain's native coin. */
  address: string;
  symbol: string;
  name: string;
  decimals: number;
  balance: number;
  usdValue: number;
}

export interface AssetNetwork {
  chainId: number;
  token: LiFiToken;
  /** Other verified tokens of this coin on this chain (e.g. Celo's 3 USDCs); a held one is swapped in. */
  alternates?: LiFiToken[];
  /** Held on this network, when the wallet holds any. */
  balance?: number;
  usdValue?: number;
}

export interface Asset {
  /** coinKey, or `chainId:address` for a token LiFi has not grouped. */
  key: string;
  symbol: string;
  name: string;
  logoURI?: string;
  verified: boolean;
  networks: AssetNetwork[];
  /** Total held across networks. */
  balance: number;
  usdValue: number;
}

/** True when `token` is its chain's native coin, on any chain family. */
export function isNativeLiFiToken(token: Pick<LiFiToken, 'address' | 'chainId'>): boolean {
  const chainId = token.chainId ?? 0;
  if (isBtcLiFiChain(chainId)) return true; // Bitcoin carries exactly one asset
  if (isSolanaLiFiChain(chainId)) return token.address === SOLANA_NATIVE_ADDRESS;
  if (isSuiLiFiChain(chainId)) return token.address.toLowerCase().endsWith('::sui::sui');
  return isNativeTokenAddress(token.address);
}

const isVerified = (t: LiFiToken) => t.verificationStatus === 'verified';

/**
 * Only a VERIFIED token may join a coin by its coinKey. LiFi gives unverified
 * lookalikes the real coin's key too (seen live: 74 of them carry USDC, USDT,
 * SOL or BNB), so trusting the key alone would file a fake under real USDC.
 */
const assetKey = (t: LiFiToken) =>
  (t.coinKey && isVerified(t) ? t.coinKey.toUpperCase() : `${t.chainId ?? 0}:${t.address.toLowerCase()}`);

/** One Asset per coinKey, each network listed once. Hidden coins are dropped. */
export function groupAssets(tokens: LiFiToken[]): Asset[] {
  const byKey = new Map<string, Asset>();
  for (const t of tokens) {
    if (t.chainId == null) continue;
    const key = assetKey(t);
    if (HIDDEN_COIN_KEYS.has(key)) continue;
    let asset = byKey.get(key);
    if (!asset) {
      asset = {
        key,
        symbol: t.coinKey ?? t.symbol,
        name: t.name,
        logoURI: t.logoURI || undefined,
        verified: t.verificationStatus === 'verified',
        networks: [],
        balance: 0,
        usdValue: 0,
      };
      byKey.set(key, asset);
    }
    const existing = asset.networks.find(n => n.chainId === t.chainId);
    if (!existing) {
      asset.networks.push({ chainId: t.chainId, token: t });
      asset.logoURI = asset.logoURI || t.logoURI || undefined;
    } else if (existing.token.address.toLowerCase() !== t.address.toLowerCase()) {
      existing.alternates = [...(existing.alternates ?? []), t];
    }
  }
  return [...byKey.values()];
}

const holdingMatches = (h: Holding, token: LiFiToken) =>
  h.chainId === token.chainId &&
  (h.address === ''
    ? isNativeLiFiToken(token)
    : h.address.toLowerCase() === token.address.toLowerCase());

/**
 * Copies `assets` with balances attached from `holdings`. Returns the holdings
 * no asset matched too, so the caller can list them on their own.
 */
export function attachHoldings(assets: Asset[], holdings: Holding[]): { assets: Asset[]; unmatched: Holding[] } {
  const used = new Set<Holding>();
  const out = assets.map(a => {
    let balance = 0;
    let usdValue = 0;
    const networks = a.networks.map(n => {
      // The token the wallet actually holds wins over the chain's other copies.
      const candidates = [n.token, ...(n.alternates ?? [])];
      for (const token of candidates) {
        const h = holdings.find(x => x.balance > 0 && holdingMatches(x, token));
        if (!h) continue;
        used.add(h);
        balance += h.balance;
        usdValue += h.usdValue;
        return { ...n, token, balance: h.balance, usdValue: h.usdValue };
      }
      return { ...n, balance: undefined, usdValue: undefined };
    });
    return { ...a, networks, balance, usdValue };
  });
  return { assets: out, unmatched: holdings.filter(h => h.balance > 0 && !used.has(h)) };
}

/** LiFi's address for a chain's native coin, given the chain id. */
export function nativeAddressFor(chainId: number): string {
  if (isSolanaLiFiChain(chainId)) return SOLANA_NATIVE_ADDRESS;
  if (isBtcLiFiChain(chainId)) return BTC_NATIVE_ADDRESS;
  if (isSuiLiFiChain(chainId)) return SUI_NATIVE_ADDRESS;
  return EVM_NATIVE_ADDRESS;
}

/**
 * An Asset for a held token LiFi's lists did not include (an imported token,
 * or a native coin that is not a major, like S on Sonic). A native row arrives
 * with an empty address; it becomes the chain's real native address, since a
 * quote for token '' can never be priced.
 */
export function assetFromHolding(h: Holding): Asset {
  const address = h.address || nativeAddressFor(h.chainId);
  const token: LiFiToken = {
    address,
    symbol: h.symbol,
    name: h.name,
    decimals: h.decimals,
    logoURI: '',
    chainId: h.chainId,
  };
  return {
    key: `${h.chainId}:${address.toLowerCase()}`,
    symbol: h.symbol,
    name: h.name,
    verified: false,
    networks: [{ chainId: h.chainId, token, balance: h.balance, usdValue: h.usdValue }],
    balance: h.balance,
    usdValue: h.usdValue,
  };
}

const preferenceRank = (chainId: number) => {
  const i = NETWORK_PREFERENCE.indexOf(chainId);
  return i === -1 ? NETWORK_PREFERENCE.length : i;
};

/**
 * The networks this side may offer for `asset`, best first: the paying side
 * only where this device can sign, and only where `allow` agrees; held
 * networks first (largest balance first), then the preference order.
 */
export function networksForSide(
  asset: Asset,
  side: PickerSide,
  allow?: (chainId: number) => boolean,
): AssetNetwork[] {
  return asset.networks
    .filter(n => (side === 'to' || isSignableSourceChain(n.chainId)) && (!allow || allow(n.chainId)))
    .sort((a, b) => {
      const aHeld = (a.balance ?? 0) > 0;
      const bHeld = (b.balance ?? 0) > 0;
      if (aHeld !== bHeld) return aHeld ? -1 : 1;
      if (aHeld) return (b.usdValue ?? 0) - (a.usdValue ?? 0);
      return preferenceRank(a.chainId) - preferenceRank(b.chainId);
    });
}

/**
 * Major coins only: verified tokens whose coinKey is in MAJOR_COIN_KEYS. The app
 * enforces this itself so an API that ignored the coinKeys filter (an older
 * build) can never fill the section with whatever tokens came back.
 */
export function onlyMajorTokens(tokens: LiFiToken[]): LiFiToken[] {
  const majors = new Set<string>(MAJOR_COIN_KEYS);
  return tokens.filter(t => isVerified(t) && !!t.coinKey && majors.has(t.coinKey.toUpperCase()));
}

export interface AssetSection {
  id: 'tsa' | 'major' | 'wallet' | 'search';
  title: string;
  assets: Asset[];
}

/**
 * The picker's default sections: On TSA (Spot listings), then the major coins
 * in MAJOR_COIN_KEYS order, then other coins the wallet holds. A coin appears
 * once, in the first section that has it. Coins with no network this side can
 * offer are left out.
 */
export function buildSections({
  listed,
  majors,
  held,
  side,
  allow,
}: {
  /** Coins the admin lists on Spot, with ALL their networks (grouped together with the majors). */
  listed: Asset[];
  majors: Asset[];
  held: Asset[];
  side: PickerSide;
  allow?: (chainId: number) => boolean;
}): AssetSection[] {
  const seen = new Set<string>();
  const usable = (list: Asset[]) =>
    list.filter(a => {
      if (seen.has(a.key) || networksForSide(a, side, allow).length === 0) return false;
      seen.add(a.key);
      return true;
    });

  const order = new Map<string, number>(MAJOR_COIN_KEYS.map((k, i) => [k, i]));
  const sortedMajors = [...majors].sort((a, b) => (order.get(a.key) ?? 99) - (order.get(b.key) ?? 99));
  const sortedHeld = [...held].sort((a, b) => b.usdValue - a.usdValue);

  return [
    { id: 'tsa' as const, title: 'On TSA', assets: usable(listed) },
    { id: 'major' as const, title: 'Stablecoins & major coins', assets: usable(sortedMajors) },
    { id: 'wallet' as const, title: 'In your wallet', assets: usable(sortedHeld) },
  ].filter(s => s.assets.length > 0);
}
