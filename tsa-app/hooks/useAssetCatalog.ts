// hooks/useAssetCatalog.ts
//
// Everything the asset picker shows, loaded by the picker itself so a screen
// only says which side it is and what to do with the choice:
//
//   - "On TSA": the admin's Spot listings (base legs), resolved to LiFi tokens
//   - "Stablecoins & major coins": MAJOR_COIN_KEYS, verified tokens only
//   - "In your wallet": other coins the wallet holds
//   - search: by name/symbol through the all-chains endpoint, or by pasted
//     contract address looked up across chains
//
// Grouping and ordering live in services/assetCatalog (pure, tested).

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  getLiFiAllChainTokens, getLiFiChainsCached, getLiFiToken, isSignableSourceChain,
  isNonEVMLiFiChain, isSolanaLiFiChain, isSuiLiFiChain, type LiFiToken,
} from '@/services/lifi';
import {
  MAJOR_COIN_KEYS, groupAssets, attachHoldings, assetFromHolding, buildSections, networksForSide,
  onlyMajorTokens, type Asset, type AssetSection, type PickerSide,
} from '@/services/assetCatalog';
import { useSpotListings } from '@/hooks/useSpotListings';
import { useWalletHoldings } from '@/hooks/useWalletHoldings';
import { resolveLiFiChainId } from '@/utils/spotChains';
import type { SpotListing } from '@/services/spotApi';

// Majors change only when LiFi's list does; one read per app session is plenty.
let majorsCache: Promise<LiFiToken[]> | null = null;
const MAJORS_PAGE = 500;
function loadMajorTokens(): Promise<LiFiToken[]> {
  if (!majorsCache) {
    majorsCache = (async () => {
      const tokens: LiFiToken[] = [];
      // Page to the end rather than trust one page to hold them all (80 today).
      for (let offset = 0, total = Infinity; offset < total && offset < MAJORS_PAGE * 5; offset += MAJORS_PAGE) {
        const page = await getLiFiAllChainTokens({ coinKeys: [...MAJOR_COIN_KEYS], limit: MAJORS_PAGE, offset });
        tokens.push(...page.tokens);
        total = page.total;
        if (page.tokens.length === 0) break;
      }
      // Enforced here too: an API that ignored coinKeys must not fill the section.
      return onlyMajorTokens(tokens);
    })().catch(err => { majorsCache = null; throw err; });
  }
  return majorsCache;
}

const listingTokenCache = new Map<string, Promise<LiFiToken | null>>();
/** The LiFi token for a Spot listing's base leg: exact symbol on its chain, verified first. */
function loadListingToken(listing: SpotListing): Promise<LiFiToken | null> {
  const chainId = resolveLiFiChainId(listing.chain);
  if (!chainId) return Promise.resolve(null);
  const key = `${chainId}:${listing.baseSymbol.toUpperCase()}`;
  let hit = listingTokenCache.get(key);
  if (!hit) {
    hit = getLiFiAllChainTokens({ q: listing.baseSymbol, chains: [chainId], limit: 50 })
      .then(page => {
        // Verified only: a listing must never resolve to a lookalike of its coin.
        const token = page.tokens.find(t =>
          t.symbol.toUpperCase() === listing.baseSymbol.toUpperCase() && t.verificationStatus === 'verified') ?? null;
        return token ? { ...token, chainId, logoURI: token.logoURI || listing.logoUrl || '' } : null;
      })
      .catch(() => { listingTokenCache.delete(key); return null; });
    listingTokenCache.set(key, hit);
  }
  return hit;
}

export type AddressKind = 'evm' | 'sui' | 'solana';

/** What kind of contract address `q` is, if any: EVM 0x…, Sui 0x…::module::T, or a Solana mint. */
export function addressKind(q: string): AddressKind | null {
  const s = q.trim();
  if (/^0x[0-9a-fA-F]{40}$/.test(s)) return 'evm';
  if (/^0x[0-9a-fA-F]+::\w+::\w+$/.test(s)) return 'sui';
  if (/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(s)) return 'solana';
  return null;
}

/** Whether a chain can hold an address of this kind (so we only ask chains that could answer). */
function chainFitsAddress(chainId: number, kind: AddressKind): boolean {
  if (kind === 'sui') return isSuiLiFiChain(chainId);
  if (kind === 'solana') return isSolanaLiFiChain(chainId);
  return !isNonEVMLiFiChain(chainId);
}

interface Options {
  visible: boolean;
  side: PickerSide;
  allow?: (chainId: number) => boolean;
  query: string;
}

export function useAssetCatalog({ visible, side, allow, query }: Options) {
  const { listings } = useSpotListings();
  const { holdings } = useWalletHoldings(visible);

  const [majorTokens, setMajorTokens] = useState<LiFiToken[]>([]);
  const [listedTokens, setListedTokens] = useState<LiFiToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    setError(null);
    loadMajorTokens()
      .then(t => { if (!cancelled) setMajorTokens(t); })
      .catch(() => { if (!cancelled) setError('Could not load coins. Check your connection and try again.'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [visible]);

  // Keyed on what the listings say, not the array's identity, so a refetch
  // that returns the same listings does not re-resolve them.
  const enabledListings = listings.filter(l => l.enabled);
  const listingsKey = enabledListings.map(l => `${l.id}:${l.chain}:${l.baseSymbol}`).join('|');
  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    Promise.all(enabledListings.map(loadListingToken)).then(tokens => {
      if (!cancelled) setListedTokens(tokens.filter((t): t is LiFiToken => t !== null));
    });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- listingsKey stands for enabledListings' content.
  }, [visible, listingsKey]);

  // Search, debounced, and only the latest answer may land.
  const [searchAssets, setSearchAssets] = useState<Asset[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);
  const searchSeq = useRef(0);
  useEffect(() => {
    const q = query.trim();
    const seq = ++searchSeq.current;
    // A new query never shows the previous one's results while it loads.
    setSearchAssets(null);
    setSearchError(null);
    if (!visible || q.length < 2) {
      setSearching(false);
      return;
    }
    setSearching(true);
    const timer = setTimeout(async () => {
      let tokens: LiFiToken[] = [];
      let failed = false;
      try {
        const kind = addressKind(q);
        if (kind) {
          // Only the chains that can hold this kind of address, and that this
          // side could use.
          const chains = (await getLiFiChainsCached()).filter(c =>
            chainFitsAddress(c.id, kind) &&
            (side === 'to' || isSignableSourceChain(c.id)) &&
            (!allow || allow(c.id)));
          const hits = await Promise.allSettled(chains.map(c => getLiFiToken(c.id, q).then(t => (t ? { ...t, chainId: c.id } : null))));
          tokens = hits.flatMap(h => (h.status === 'fulfilled' && h.value ? [h.value] : []));
        } else {
          tokens = (await getLiFiAllChainTokens({ q, limit: 100 })).tokens;
        }
      } catch {
        failed = true;
      }
      if (seq !== searchSeq.current) return;
      setSearchAssets(failed ? null : groupAssets(tokens));
      setSearchError(failed ? 'Search failed. Check your connection and try again.' : null);
      setSearching(false);
    }, 300);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- allow is read at search time; a new function identity must not re-run the search.
  }, [visible, query, side]);

  const sections: AssetSection[] = useMemo(() => {
    const withHoldings = (list: Asset[]) => attachHoldings(list, holdings).assets;

    if (query.trim().length >= 2) {
      if (!searchAssets) return [];
      const found = withHoldings(searchAssets)
        .filter(a => networksForSide(a, side, allow).length > 0)
        // Verified coins first, then what the wallet holds, then the rest.
        .sort((a, b) => Number(b.verified) - Number(a.verified) || b.usdValue - a.usdValue);
      return found.length ? [{ id: 'search', title: 'Search results', assets: found }] : [];
    }

    // Listed and major tokens are grouped TOGETHER, so a coin listed on one
    // chain still shows every network it has; the listing only decides that
    // it sits under "On TSA".
    const known = attachHoldings(groupAssets([...listedTokens, ...majorTokens]), holdings);
    const listedAddrs = new Set(listedTokens.map(t => `${t.chainId}:${t.address.toLowerCase()}`));
    const isListed = (a: Asset) => a.networks.some(n => listedAddrs.has(`${n.chainId}:${n.token.address.toLowerCase()}`)
      || (n.alternates ?? []).some(t => listedAddrs.has(`${n.chainId}:${t.address.toLowerCase()}`)));
    const listed = known.assets.filter(isListed);
    const majors = known.assets.filter(a => !isListed(a));
    // Held coins not already covered, including tokens LiFi's lists lack.
    const held = known.unmatched.map(assetFromHolding);
    return buildSections({ listed, majors, held, side, allow });
  }, [query, searchAssets, listedTokens, majorTokens, holdings, side, allow]);

  const isSearch = query.trim().length >= 2;
  return { sections, loading: loading && !majorTokens.length, searching, error: isSearch ? searchError : error };
}

/** Test seam: forget module-level caches. */
export function resetAssetCatalogCache() {
  majorsCache = null;
  listingTokenCache.clear();
}
