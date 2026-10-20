// Paged Promotions Arena feed with a kind filter.
//
// The API filters by `type` (Product | Service) only. The three service kinds
// (contact / order / booking) live on the listing's CATEGORY, so those chips ask
// for Services and narrow client-side by the category's serviceType — the same
// field the marketplace tab uses. A page that narrows to nothing keeps paging
// (bounded) so the list does not look empty while later pages hold matches.
import { useCallback, useEffect, useRef, useState } from 'react';
import api, { Product } from '@/components/services/api';

export type PromoFilter = 'all' | 'product' | 'order' | 'booking' | 'contact';

const PAGE_SIZE = 20;
const MAX_SKIPPED_PAGES = 5;

export const categoryIdOf = (p: Product) =>
  typeof p.category === 'string' ? p.category : p.category?.id;

export function matchesFilter(p: Product, filter: PromoFilter, serviceTypes: Record<string, string>) {
  if (filter === 'all' || filter === 'product') return true; // already narrowed by `type`
  const kind = serviceTypes[categoryIdOf(p) ?? ''] ?? 'contact';
  return kind === filter;
}

export function usePromotions(filter: PromoFilter) {
  const [items, setItems] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const page = useRef(0);
  const busy = useRef(false);
  const serviceTypes = useRef<Record<string, string> | null>(null);
  // Bumped on every filter change/refresh so a slow response from a previous
  // request cannot overwrite the current list.
  const generation = useRef(0);

  const kinds = useCallback(async () => {
    if (serviceTypes.current) return serviceTypes.current;
    const res = await api.getCategories({ active: true });
    const map: Record<string, string> = {};
    (res.success && Array.isArray(res.data) ? res.data : []).forEach((c: any) => {
      if (c.serviceType) map[c.id] = c.serviceType;
    });
    serviceTypes.current = map;
    return map;
  }, []);

  const fetchPages = useCallback(async (reset: boolean) => {
    if (busy.current) return;
    busy.current = true;
    const gen = ++generation.current;
    if (reset) page.current = 0;
    setError(null);
    try {
      const type = filter === 'all' ? undefined : filter === 'product' ? 'Product' : 'Service';
      const narrowed = filter !== 'all' && filter !== 'product';
      const types = narrowed ? await kinds() : {};
      let found: Product[] = [];
      let more = true;
      for (let skipped = 0; more && found.length === 0 && skipped < MAX_SKIPPED_PAGES; skipped++) {
        const res = await api.getPromotions({ page: page.current + 1, limit: PAGE_SIZE, type });
        if (!res.success || !res.data) throw new Error(res.message || 'Could not load promotions');
        page.current += 1;
        const { products, pagination } = res.data;
        more = page.current * (pagination?.limit ?? PAGE_SIZE) < (pagination?.total ?? 0);
        found = products.filter((p) => matchesFilter(p, filter, types));
      }
      if (gen !== generation.current) return;
      setItems((prev) => (reset ? found : [...prev, ...found]));
      setHasMore(more);
    } catch (e: any) {
      if (gen === generation.current) setError(e?.message || 'Could not load promotions');
    } finally {
      busy.current = false;
      if (gen === generation.current) {
        setLoading(false);
        setRefreshing(false);
        setLoadingMore(false);
      }
    }
  }, [filter, kinds]);

  useEffect(() => {
    busy.current = false;
    setLoading(true);
    setItems([]);
    fetchPages(true);
  }, [fetchPages]);

  const refresh = useCallback(() => {
    setRefreshing(true);
    fetchPages(true);
  }, [fetchPages]);

  const loadMore = useCallback(() => {
    if (!hasMore || busy.current || error) return;
    setLoadingMore(true);
    fetchPages(false);
  }, [hasMore, error, fetchPages]);

  return { items, loading, refreshing, loadingMore, error, refresh, loadMore };
}
