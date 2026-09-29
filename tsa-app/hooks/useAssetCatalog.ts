// hooks/useAssetCatalog.ts
//
// Everything the asset picker shows, loaded by the picker itself so a screen
// only says which side it is and what to do with the choice:
//
//   - "On TSA": the admin's Spot listings (base legs), resolved to LiFi tokens
//   - "Stablecoins & major coins": MAJOR_COIN_KEYS, verified tokens only
//   - "In your wallet": other coins the wallet holds — ours, and any other token
//     it holds that LiFi can swap
//   - search: by name/symbol through the all-chains endpoint, or by pasted
//     contract address looked up across chains
//
// Grouping and ordering live in services/assetCatalog (pure, tested).
//
// With `holdingsOnly` the same sections carry only what the wallet holds, and
// search is off — the sell flow picks from her balances, not the catalogue.
//
// On the receiving side MCGP shows the Spendable MCGP balance, the only MCGP
// usable in the app, not wallet MCGP. Never on the paying side: there the
// balance is what the flow will move (All Swap spends wallet MCGP; the sell and
// pay flows never offer it).
//
// Opening fast: the majors and the resolved On TSA coins are kept on the device
// and shown at once on the next launch, and the reads start when the screen
// mounts the picker, not when the sheet opens.

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  getLiFiAllChainTokens, getLiFiChainsCached, getLiFiToken, isSignableSourceChain,
  isNonEVMLiFiChain, isSolanaLiFiChain, isSuiLiFiChain, type LiFiToken,
} from '@/services/lifi';
import {
  MAJOR_COIN_KEYS, groupAssets, attachHoldings, assetFromHolding, buildSections, networksForSide,
  onlyMajorTokens, type Asset, type AssetSection, type PickerSide,
} from '@/services/assetCatalog';
import { withSpendableMcgp } from '@/utils/walletMcgp';
import { resetSpendableMcgpCache, useSpendableMcgp } from '@/hooks/useSpendableMcgp';
import { resetSwappableHeldCache, useSwappableHeld } from '@/hooks/useSwappableHeld';
import { useSpotListings } from '@/hooks/useSpotListings';
import { prefetchWalletHoldings, useWalletHoldings } from '@/hooks/useWalletHoldings';
import { persistedCache } from '@/services/persistedCache';
import { resolveLiFiChainId } from '@/utils/spotChains';
import type { SpotListing } from '@/services/spotApi';

// The backend refreshes LiFi's list (and the prices on it) daily; the device
// copy is re-read after half that, so a shown price is never much older.
const STORED_TTL_MS = 12 * 60 * 60 * 1000;
const isTokenList = (v: unknown): v is LiFiToken[] =>
  Array.isArray(v) && v.every(t => typeof t?.address === 'string' && typeof t?.symbol === 'string');
const storedMajors = persistedCache('asset_picker_majors_v1', STORED_TTL_MS, isTokenList);
const storedListed = persistedCache('asset_picker_listed_v1', STORED_TTL_MS, isTokenList);

const MAJORS_PAGE = 500;
async function fetchMajorTokens(): Promise<LiFiToken[]> {
  const tokens: LiFiToken[] = [];
  // Page to the end rather than trust one page to hold them all (80 today).
  for (let offset = 0, total = Infinity; offset < total && offset < MAJORS_PAGE * 5; offset += MAJORS_PAGE) {
    const page = await getLiFiAllChainTokens({ coinKeys: [...MAJOR_COIN_KEYS], limit: MAJORS_PAGE, offset });
    tokens.push(...page.tokens);
    total = page.total;
    if (page.tokens.length === 0) break;
  }
  // Enforced here too: an API that ignored coinKeys must not fill the section.
  const majors = onlyMajorTokens(tokens);
  storedMajors.write(majors);
  return majors;
}

// One read per app session, or none while the device copy is recent. A stale
// copy is still shown, and the next open gets the refreshed list.
let majorsCache: Promise<LiFiToken[]> | null = null;
function loadMajorTokens(): Promise<LiFiToken[]> {
  if (!majorsCache) {
    majorsCache = (async () => {
      const hit = await storedMajors.read();
      if (!hit) return fetchMajorTokens();
      if (!hit.fresh) {
        fetchMajorTokens().then(fresh => { majorsCache = Promise.resolve(fresh); }).catch(() => {});
      }
      return onlyMajorTokens(hit.value);
    })().catch(err => { majorsCache = null; throw err; });
  }
  return majorsCache;
}

// The last resolved On TSA coins, shown until the listings are known.
let lastListed: Promise<LiFiToken[]> | null = null;
const loadLastListed = () => (lastListed ??= storedListed.read().then(hit => hit?.value ?? []));

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
  allow?: (chainId: number, token?: LiFiToken) => boolean;
  query: string;
  /**
   * Only coins the wallet holds — what the sell flow picks from. The catalogue
   * search is not run at all: searching every coin there is says nothing about
   * what she can sell.
   */
  holdingsOnly?: boolean;
}

export function useAssetCatalog({ visible, side, allow, query, holdingsOnly = false }: Options) {
  const { listings, loading: listingsLoading, error: listingsError } = useSpotListings();
  const { holdings, loading: holdingsLoading } = useWalletHoldings(visible);

  const [majorTokens, setMajorTokens] = useState<LiFiToken[]>([]);
  const [listedTokens, setListedTokens] = useState<LiFiToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Runs on mount, while the sheet is still closed, and again on open, which
  // is instant once loaded and a retry if the first read failed.
  useEffect(() => {
    let cancelled = false;
    setError(null);
    // Harmless once coins are shown: only an empty list turns it into a spinner.
    setLoading(true);
    loadMajorTokens()
      .then(t => { if (!cancelled) setMajorTokens(t); })
      .catch(() => { if (!cancelled) setError('Could not load coins. Check your connection and try again.'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [visible]);

  useEffect(() => { prefetchWalletHoldings().catch(() => {}); }, []);

  // Keyed on what the listings say, not the array's identity, so a refetch
  // that returns the same listings does not re-resolve them.
  const enabledListings = listings.filter(l => l.enabled);
  const listingsKey = enabledListings.map(l => `${l.id}:${l.chain}:${l.baseSymbol}`).join('|');
  // No listings yet because they are loading or failed is not "nothing is
  // listed": the stored coins stand until the listings are actually known.
  const listingsUnknown = listings.length === 0 && (!!listingsLoading || !!listingsError);
  useEffect(() => {
    let cancelled = false;
    const resolved = listingsUnknown
      ? loadLastListed()
      : Promise.all(enabledListings.map(loadListingToken)).then(tokens => {
        const listed = tokens.filter((t): t is LiFiToken => t !== null);
        lastListed = Promise.resolve(listed);
        storedListed.write(listed);
        return listed;
      });
    resolved.then(listed => { if (!cancelled) setListedTokens(listed); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- listingsKey stands for enabledListings' content.
  }, [listingsKey, listingsUnknown]);

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
    if (!visible || holdingsOnly || q.length < 2) {
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
  }, [visible, query, side, holdingsOnly]);

  // The balances the list shows (see the MCGP note at the top).
  const showsSpendableMcgp = side === 'to' && !holdingsOnly;
  const spendableMcgp = useSpendableMcgp(visible && showsSpendableMcgp);
  const shownHoldings = useMemo(
    () => (showsSpendableMcgp ? withSpendableMcgp(holdings, spendableMcgp) : holdings),
    [holdings, showsSpendableMcgp, spendableMcgp],
  );

  // Listed and major tokens are grouped TOGETHER, so a coin listed on one
  // chain still shows every network it has; the listing only decides that
  // it sits under "On TSA".
  const known = useMemo(
    () => attachHoldings(groupAssets([...listedTokens, ...majorTokens]), shownHoldings),
    [listedTokens, majorTokens, shownHoldings],
  );
  const unvouched = useMemo(() => known.unmatched.filter(h => !h.trusted), [known]);
  const swappableHeld = useSwappableHeld(unvouched, visible);

  const sections: AssetSection[] = useMemo(() => {
    const withHoldings = (list: Asset[]) => attachHoldings(list, shownHoldings).assets;

    if (!holdingsOnly && query.trim().length >= 2) {
      if (!searchAssets) return [];
      const found = withHoldings(searchAssets)
        .filter(a => networksForSide(a, side, allow).length > 0)
        // Verified coins first, then what the wallet holds, then the rest.
        .sort((a, b) => Number(b.verified) - Number(a.verified) || b.usdValue - a.usdValue);
      return found.length ? [{ id: 'search', title: 'Search results', assets: found }] : [];
    }

    const listedAddrs = new Set(listedTokens.map(t => `${t.chainId}:${t.address.toLowerCase()}`));
    const isListed = (a: Asset) => a.networks.some(n => listedAddrs.has(`${n.chainId}:${n.token.address.toLowerCase()}`)
      || (n.alternates ?? []).some(t => listedAddrs.has(`${n.chainId}:${t.address.toLowerCase()}`)));
    const listed = known.assets.filter(isListed);
    const majors = known.assets.filter(a => !isListed(a));
    // Held coins not already covered: ours as they are (LiFi's lists may lack
    // them), the rest as LiFi knows them, so a verified one is labelled so.
    const held = [...known.unmatched.filter(h => h.trusted).map(assetFromHolding), ...swappableHeld];
    return buildSections({ listed, majors, held, side, allow, holdingsOnly });
  }, [query, searchAssets, listedTokens, known, swappableHeld, shownHoldings, side, allow, holdingsOnly]);

  // Whether `allow` keeps back a coin the wallet actually holds: the only case
  // in which a picker's "why isn't X here" note says anything.
  const hidesHeld = useMemo(
    () => !!allow && holdings.some(h => h.balance > 0
      && assetFromHolding(h).networks.every(n => !allow(n.chainId, n.token))),
    [holdings, allow],
  );

  const isSearch = !holdingsOnly && query.trim().length >= 2;
  // In holdings mode the list IS her balances: until they have loaded, an empty
  // list would read as "you hold nothing", so it counts as loading.
  const catalogLoading = loading && !majorTokens.length;
  return {
    sections,
    loading: catalogLoading || (holdingsOnly && holdingsLoading),
    searching,
    error: isSearch ? searchError : error,
    hidesHeld,
    /** MCGP balances in `sections` are Spendable MCGP, not wallet MCGP. */
    showsSpendableMcgp,
  };
}

/** Test seam: forget module-level caches. */
export function resetAssetCatalogCache() {
  majorsCache = null;
  lastListed = null;
  listingTokenCache.clear();
  resetSpendableMcgpCache();
  resetSwappableHeldCache();
}
