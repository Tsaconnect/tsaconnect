// hooks/useSpotListings.ts
//
// One fetch of /spot-listings shared by every consumer. Three screens want
// three different projections of the same admin-curated dataset (the Spot list,
// the wallet category filter, the homepage pair picker); before this they each
// fetched it independently, so Home → Wallet → Spot cost three identical round
// trips.
//
// Cache is module-level with a TTL: listings change only when an admin edits
// them, so a short staleness window is fine and pull-to-refresh can force a
// refetch. In-flight requests are shared so concurrent mounts issue one call.
import { useCallback, useEffect, useState } from 'react';
import { getSpotListings, type SpotListing } from '@/services/spotApi';

const TTL_MS = 60_000;

let cache: { listings: SpotListing[]; at: number } | null = null;
let inFlight: Promise<{ listings: SpotListing[] | null; error: string | null }> | null = null;

async function fetchListings(force: boolean) {
  if (!force && cache && Date.now() - cache.at < TTL_MS) {
    return { listings: cache.listings, error: null };
  }
  if (inFlight) return inFlight;

  inFlight = (async () => {
    const r = await getSpotListings();
    if (r.success && r.data) {
      cache = { listings: r.data.listings ?? [], at: Date.now() };
      return { listings: cache.listings, error: null };
    }
    // Serve stale data over an error when we have it — a blip shouldn't blank
    // a working screen.
    if (cache) return { listings: cache.listings, error: null };
    return { listings: null, error: r.message || 'Could not load trading pairs' };
  })();

  try {
    return await inFlight;
  } finally {
    inFlight = null;
  }
}

export function useSpotListings() {
  const [listings, setListings] = useState<SpotListing[]>(cache?.listings ?? []);
  const [loading, setLoading] = useState(!cache);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (force = false) => {
    setLoading(true);
    const r = await fetchListings(force);
    if (r.listings) {
      setListings(r.listings);
      setError(null);
    } else {
      setError(r.error);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return { listings, loading, error, reload: () => load(true) };
}

/** Test-only: drop the module cache so cases don't leak into each other. */
export function __resetSpotListingsCache() {
  cache = null;
  inFlight = null;
}
