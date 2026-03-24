// hooks/useSubcategoryProducts.ts
import { useState, useEffect, useCallback } from 'react';
import api, { Product } from '@/components/services/api';

export interface SellerData {
    sellerId: string;
    name: string;
    email: string;
    companyName?: string;
    location: string;
    phoneNumber: string;
    profilePhoto?: string;
    rating: {
        average: number;
        count: number;
    };
    products: Product[];
    productCount: number;
    isVerified: boolean;
    joinedDate: string;
}

interface UseSubcategoryProductsProps {
    subcategoryId?: string;
    categoryId?: string;
    searchQuery?: string;
    page?: number;
    limit?: number;
    location?: string;
    minPrice?: number;
    maxPrice?: number;
    sort?: 'recent' | 'popular' | 'price_low' | 'price_high' | 'rating';
}

export function useSubcategoryProducts({
    subcategoryId,
    categoryId,
    searchQuery = '',
    page = 1,
    limit = 20,
    location,
    minPrice,
    maxPrice,
    sort = 'recent',
}: UseSubcategoryProductsProps) {
    const [products, setProducts] = useState<Product[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [refreshing, setRefreshing] = useState(false);
    const [totalProducts, setTotalProducts] = useState(0);
    const [hasMore, setHasMore] = useState(true);
    const [categoryInfo, setCategoryInfo] = useState<any>(null);

    // Group products by seller to create seller cards
    // hooks/useSubcategoryProducts.ts
    // In the groupProductsBySeller function, ensure rating has proper structure:
    const groupProductsBySeller = useCallback((products: Product[]): SellerData[] => {
        const sellersMap = new Map<string, SellerData>();

        products.forEach((product) => {
            const sellerKey = product.email || product.companyName || 'unknown';

            if (!sellersMap.has(sellerKey)) {
                sellersMap.set(sellerKey, {
                    sellerId: product._id || product.id || product.userId || Math.random().toString(),
                    name: product.companyName || product.email?.split('@')[0] || 'Unknown Seller',
                    email: product.email,
                    companyName: product.companyName,
                    location: product.location,
                    phoneNumber: product.phoneNumber,
                    profilePhoto: product.images?.[0]?.url,
                    // Ensure rating has proper structure
                    rating: product.rating || { average: 0, count: 0 },
                    products: [product],
                    productCount: 1,
                    isVerified: true,
                    joinedDate: product.createdAt || new Date().toISOString(),
                });
            } else {
                const existingSeller = sellersMap.get(sellerKey)!;
                existingSeller.products.push(product);
                existingSeller.productCount += 1;
            }
        });

        return Array.from(sellersMap.values());
    }, []);

    const [sellers, setSellers] = useState<SellerData[]>([]);

    const fetchProducts = useCallback(async (isRefreshing = false) => {
        if (!subcategoryId && !categoryId) return;

        try {
            if (isRefreshing) {
                setRefreshing(true);
            } else {
                setLoading(true);
            }
            setError(null);

            // Determine if we're fetching by subcategory or category tree
            // Prefer categoryId (parent category) over subcategoryId when both are present
            let response;

            if (categoryId) {
                // Fetch products by category tree (includes all subcategories)
                response = await api.getProductsByCategoryTree(categoryId, true);
            } else if (subcategoryId) {
                // Fetch products by specific subcategory
                response = await api.getProductsByCategory({
                    subcategoryId,
                    page,
                    limit,
                    search: searchQuery || undefined,
                    minPrice,
                    maxPrice,
                    sortBy: sort === 'recent' ? 'createdAt' :
                        sort === 'popular' ? 'sales' :
                            sort === 'price_low' ? 'price' :
                                sort === 'price_high' ? 'price' :
                                    sort === 'rating' ? 'rating.average' : 'createdAt',
                    sortOrder: sort === 'price_high' ? 'desc' : 'asc',
                    status: 'active',
                });
            }

            if (response?.success && response.data) {
                if (categoryId) {
                    const data = response.data as any;
                    // Flatten all products from all subcategories
                    const allProducts: Product[] = [];
                    if (data.groupedBySubcategory) {
                        Object.values(data.groupedBySubcategory).forEach((group: any) => {
                            const items = Array.isArray(group) ? group : group?.products || [];
                            allProducts.push(...items);
                        });
                    }
                    setProducts(allProducts);
                    setCategoryInfo(data.mainCategory);
                    setTotalProducts(allProducts.length);
                    setHasMore(false);

                    // Group products by seller
                    const groupedSellers = groupProductsBySeller(allProducts);
                    setSellers(groupedSellers);
                } else {
                    const data = response.data as any;
                    setProducts(data.products || []);
                    setCategoryInfo(data.category);
                    setTotalProducts(data.pagination?.total || data.products?.length || 0);
                    setHasMore(data.pagination?.page < data.pagination?.pages);

                    // Group products by seller
                    const groupedSellers = groupProductsBySeller(data.products || []);
                    setSellers(groupedSellers);
                }
            } else {
                throw new Error(response?.message || 'Failed to fetch products');
            }
        } catch (err: any) {
            console.error('Error fetching subcategory products:', err);
            setError(err.message || 'Failed to load products');

            // Fallback to empty state
            setProducts([]);
            setSellers([]);
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, [subcategoryId, categoryId, searchQuery, page, limit, location, minPrice, maxPrice, sort, groupProductsBySeller]);

    useEffect(() => {
        fetchProducts();
    }, [fetchProducts]);

    const handleRefresh = useCallback(() => {
        fetchProducts(true);
    }, [fetchProducts]);

    const loadMore = useCallback(() => {
        if (!hasMore || loading) return;
        // Implement pagination if needed
    }, [hasMore, loading]);

    // Filter sellers by search query
    const filteredSellers = sellers.filter(seller => {
        if (!searchQuery) return true;

        const query = searchQuery.toLowerCase();
        return (
            seller.name.toLowerCase().includes(query) ||
            seller.email.toLowerCase().includes(query) ||
            seller.companyName?.toLowerCase().includes(query) ||
            seller.location.toLowerCase().includes(query) ||
            seller.products.some(product =>
                product.name.toLowerCase().includes(query) ||
                product.description.toLowerCase().includes(query)
            )
        );
    });

    return {
        products,
        sellers: filteredSellers,
        loading,
        error,
        refreshing,
        totalProducts,
        showingProducts: filteredSellers.length,
        categoryInfo,
        handleRefresh,
        loadMore,
        hasMore,
    };
}