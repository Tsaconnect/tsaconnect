// Paged Promotions Arena feed. Promotions are products only for now (the API
// refuses promos on services), so the feed always asks for type=Product.
import { useCallback, useEffect, useRef, useState } from 'react';
import api, { Product } from '@/components/services/api';

const PAGE_SIZE = 20;

export function usePromotions() {
  const [items, setItems] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const page = useRef(0);
  const busy = useRef(false);
  // Bumped on every refresh so a slow response from a previous request cannot
  // overwrite the current list.
  const generation = useRef(0);

  const fetchPage = useCallback(async (reset: boolean) => {
    if (busy.current) return;
    busy.current = true;
    const gen = ++generation.current;
    if (reset) page.current = 0;
    setError(null);
    try {
      const res = await api.getPromotions({ page: page.current + 1, limit: PAGE_SIZE, type: 'Product' });
      if (!res.success || !res.data) throw new Error(res.message || 'Could not load promotions');
      page.current += 1;
      const { products, pagination } = res.data;
      const more = page.current * (pagination?.limit ?? PAGE_SIZE) < (pagination?.total ?? 0);
      if (gen !== generation.current) return;
      setItems((prev) => (reset ? products : [...prev, ...products]));
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
  }, []);

  useEffect(() => {
    fetchPage(true);
  }, [fetchPage]);

  const refresh = useCallback(() => {
    setRefreshing(true);
    fetchPage(true);
  }, [fetchPage]);

  const loadMore = useCallback(() => {
    if (!hasMore || busy.current || error) return;
    setLoadingMore(true);
    fetchPage(false);
  }, [hasMore, error, fetchPage]);

  return { items, loading, refreshing, loadingMore, error, refresh, loadMore };
}
