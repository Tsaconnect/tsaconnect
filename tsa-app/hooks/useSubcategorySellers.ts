import { useState, useEffect, useCallback } from 'react';
import { SellerWithProducts } from '@/types/marketplace';
import { generateDummySellers, searchDummyData } from '@/data/dummyMarketplace';

interface UseSubcategorySellersProps {
  subcategoryId: string;
  searchQuery?: string;
  delay?: number;
}

export const useSubcategorySellers = ({
  subcategoryId,
  searchQuery = '',
  delay = 600 // Simulate network delay
}: UseSubcategorySellersProps) => {
  const [sellers, setSellers] = useState<SellerWithProducts[]>([]);
  const [filteredSellers, setFilteredSellers] = useState<SellerWithProducts[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [searchLoading, setSearchLoading] = useState(false);

  const fetchSellers = useCallback(async (isRefreshing = false) => {
    try {
      if (!isRefreshing) setLoading(true);
      setError(null);
      
      // Simulate network delay
      await new Promise(resolve => setTimeout(resolve, delay));
      
      const data = generateDummySellers(subcategoryId);
      
      if (!data || data.length === 0) {
        throw new Error('No sellers found for this subcategory');
      }
      
      setSellers(data);
      setFilteredSellers(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load sellers');
      // Fallback to empty array
      setSellers([]);
      setFilteredSellers([]);
    } finally {
      setLoading(false);
      setRefreshing(false);
      setSearchLoading(false);
    }
  }, [subcategoryId, delay]);

  const handleSearch = useCallback(async (query: string) => {
    if (!query.trim()) {
      setFilteredSellers(sellers);
      return;
    }

    try {
      setSearchLoading(true);
      
      // Simulate search delay
      await new Promise(resolve => setTimeout(resolve, 300));
      
      const results = searchDummyData(subcategoryId, query);
      setFilteredSellers(results);
    } catch (err) {
      console.error('Search failed:', err);
      // Fallback to client-side filtering
      const normalizedQuery = query.toLowerCase();
      const filtered = sellers
        .map(seller => ({
          ...seller,
          products: seller.products.filter(product =>
            product.name.toLowerCase().includes(normalizedQuery) ||
            product.productId.toLowerCase().includes(normalizedQuery) ||
            product.description?.toLowerCase().includes(normalizedQuery) ||
            product.tags?.some(tag => tag.toLowerCase().includes(normalizedQuery))
          )
        }))
        .filter(seller =>
          seller.products.length > 0 ||
          seller.sellerId.toLowerCase().includes(normalizedQuery) ||
          seller.email.toLowerCase().includes(normalizedQuery) ||
          seller.name.toLowerCase().includes(normalizedQuery) ||
          seller.location?.toLowerCase().includes(normalizedQuery)
        );
      setFilteredSellers(filtered);
    } finally {
      setSearchLoading(false);
    }
  }, [subcategoryId, sellers]);

  const handleRefresh = useCallback(() => {
    setRefreshing(true);
    fetchSellers(true);
  }, [fetchSellers]);

  useEffect(() => {
    fetchSellers();
  }, [fetchSellers]);

  useEffect(() => {
    if (searchQuery !== undefined) {
      handleSearch(searchQuery);
    }
  }, [searchQuery, handleSearch]);

  return {
    sellers: filteredSellers,
    loading: loading || searchLoading,
    error,
    refreshing,
    handleRefresh,
    handleSearch,
    refetch: () => fetchSellers(true),
    totalSellers: sellers.length,
    showingSellers: filteredSellers.length,
  };
};