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

/**
 * Majors that are a chain's own coin. Only their native form counts: LiFi also
 * files bridged copies under the same key (Wormhole "Wrapped SOL" on EVM
 * chains), which are not what someone choosing "SOL" means.
 */
const NATIVE_MAJOR_KEYS = new Set(['ETH', 'BNB', 'SOL', 'BTC', 'SUI']);

/** Plain names for the majors, instead of whichever chain's token name came first. */
const MAJOR_NAMES: Record<string, string> = {
  USDC: 'USD Coin', USDT: 'Tether', ETH: 'Ethereum', BNB: 'BNB', SOL: 'Solana', BTC: 'Bitcoin', SUI: 'Sui',
};

/** Coins never offered, even from search (product decision: TRX is hidden). */
export const HIDDEN_COIN_KEYS = new Set(['TRX']);

/** Preferred network when the wallet holds a coin nowhere: Base, then these. */
const NETWORK_PREFERENCE = [8453, 56, 42161, 10, 137, 146, 1];

export type PickerSide = 'from' | 'to';

/** A wallet balance for one token on one chain. */
export interface Holding {
  chainId: number;
  /** The token's contract; '' when unknown (an older API) or for the native coin. */
  address: string;
  /** The chain's own coin (ETH on Base, S on Sonic). Never inferred from a missing address. */
  native: boolean;
  /** From a curated source (our supported-token list, or a native coin), so its registry logo may be used. */
  trusted: boolean;
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
  /**
   * Someone we trust stands behind it: LiFi verified it, or it came from our own
   * token list or configuration. Decides the Unverified label.
   */
  vouched: boolean;
  /**
   * May borrow the registry logo for its symbol. Narrower than `vouched`: a
   * bridged copy of ETH is vouched for but must not wear the ETH icon.
   */
  logoTrusted: boolean;
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
  if (isSuiLiFiChain(chainId)) return isSuiNativeCoinType(token.address);
  return isNativeTokenAddress(token.address);
}

/**
 * Whether a Sui coin type is native SUI, written short (0x2::sui::SUI) or padded
 * (0x00…02::sui::SUI) as the RPC reports it. Checking only the "::sui::SUI"
 * tail would let any package publish a coin that passes for SUI.
 */
export function isSuiNativeCoinType(coinType: string): boolean {
  const [pkg, mod, name, ...rest] = coinType.split('::');
  return rest.length === 0 && mod === 'sui' && name === 'SUI' && /^0x0*2$/i.test(pkg ?? '');
}

const isVerified = (t: LiFiToken) => t.verificationStatus === 'verified';

/** A token named like a chain's own coin (SOL, ETH…) that is not that coin; `null` = it is native. */
const isBridgedNativeMajor = (symbol: string, token: Pick<LiFiToken, 'chainId' | 'address'> | null) =>
  NATIVE_MAJOR_KEYS.has(symbol.toUpperCase()) && token !== null && !isNativeLiFiToken(token);

/**
 * Whether a token joins its coin by coinKey. Only a VERIFIED token may: LiFi
 * gives unverified lookalikes the real coin's key too (seen live: 74 of them
 * carry USDC, USDT, SOL or BNB). And a chain's own coin (SOL, ETH…) joins only
 * in its native form: LiFi files bridged copies (Wormhole "Wrapped SOL" on EVM
 * chains) under the same key, and those are not what someone choosing SOL means.
 * A token that does not join stands alone, under its own name.
 */
function joinsByCoinKey(t: LiFiToken): boolean {
  const key = t.coinKey?.toUpperCase();
  if (!key || !isVerified(t)) return false;
  return !NATIVE_MAJOR_KEYS.has(key) || isNativeLiFiToken(t);
}

const assetKey = (t: LiFiToken) =>
  (joinsByCoinKey(t) ? t.coinKey!.toUpperCase() : `${t.chainId ?? 0}:${t.address.toLowerCase()}`);

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
        symbol: joinsByCoinKey(t) ? t.coinKey! : t.symbol,
        name: MAJOR_NAMES[key] ?? t.name,
        logoURI: t.logoURI || undefined,
        verified: isVerified(t),
        vouched: isVerified(t),
        // A bridged copy keeps its own logo: our "SOL" icon would pass it off as SOL.
        logoTrusted: isVerified(t) && (joinsByCoinKey(t) || !isBridgedNativeMajor(t.symbol, t)),
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
  // The logo comes from the coin's home network (its native form, else the
  // preferred chain), not whichever chain's token happened to sort first.
  for (const asset of byKey.values()) {
    const home = [...asset.networks].sort((a, b) =>
      Number(isNativeLiFiToken(b.token)) - Number(isNativeLiFiToken(a.token)) ||
      preferenceRank(a.chainId) - preferenceRank(b.chainId))[0];
    asset.logoURI = home?.token.logoURI || asset.logoURI;
  }
  return [...byKey.values()];
}

/**
 * Whether holding `h` is `token`. A missing address is never read as "native":
 * that is how a USDC balance would turn into ETH. With no address (an older
 * API), a trusted holding may match a VERIFIED token of the same symbol on the
 * same chain, and nothing else.
 */
const holdingMatches = (h: Holding, token: LiFiToken) => {
  if (h.chainId !== token.chainId) return false;
  if (h.native) return isNativeLiFiToken(token);
  if (h.address) return h.address.toLowerCase() === token.address.toLowerCase();
  // Only until the API that sends contractAddress is deployed everywhere. If two
  // verified tokens on one chain share a symbol, the first one wins.
  return h.trusted && isVerified(token) && token.symbol.toUpperCase() === h.symbol.toUpperCase();
};

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
  // A holding we cannot pin to a contract is not offered on its own: guessing
  // its address is how the wrong token would get swapped.
  const identifiable = (h: Holding) => h.native || !!h.address;
  return { assets: out, unmatched: holdings.filter(h => h.balance > 0 && !used.has(h) && identifiable(h)) };
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
  const address = h.native ? nativeAddressFor(h.chainId) : h.address;
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
    vouched: h.trusted,
    // Binance-Peg ETH on BSC is from our own list, but it is not ETH: no ETH icon.
    logoTrusted: h.trusted && !isBridgedNativeMajor(h.symbol, h.native ? null : h),
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
  return tokens.filter(t => joinsByCoinKey(t) && majors.has(t.coinKey!.toUpperCase()));
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
