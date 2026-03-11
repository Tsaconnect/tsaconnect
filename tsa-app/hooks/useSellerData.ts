// hooks/useSellerData.ts
import { useState, useEffect, useCallback } from 'react';
import api, { Product } from '@/components/services/api';

interface SellerData {
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
    totalSales?: number;
    description?: string;
}

interface UseSellerDataProps {
    sellerEmail: string;
    sellerId?: string;
    categoryTitle?: string;
}

export function useSellerData({
    sellerEmail,
    sellerId,
    categoryTitle
}: UseSellerDataProps) {
    const [sellerData, setSellerData] = useState<SellerData | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [refreshing, setRefreshing] = useState(false);

    const fetchSellerData = useCallback(async (isRefreshing = false) => {
        if (!sellerEmail) {
            setError('Seller email is required');
            setLoading(false);
            return;
        }

        try {
            if (isRefreshing) {
                setRefreshing(true);
            } else {
                setLoading(true);
            }
            setError(null);

            // Fetch all products by this seller
            const response = await api.getMarketplaceProducts({
                search: sellerEmail,
                status: 'active',
                limit: 100,
            });

            if (response.success && response.data) {
                const products = response.data.products || [];
                const sellerProducts = products.filter(p => p.email === sellerEmail);

                if (sellerProducts.length > 0) {
                    const firstProduct = sellerProducts[0];

                    // Calculate statistics
                    const totalSales = sellerProducts.reduce((sum, product) =>
                        sum + (product.sales || 0), 0
                    );

                    const totalRating = sellerProducts.reduce((sum, product) =>
                        sum + (product.rating?.average || 0), 0
                    );
                    const avgRating = sellerProducts.length > 0 ? totalRating / sellerProducts.length : 0;

                    const totalReviews = sellerProducts.reduce((sum, product) =>
                        sum + (product.rating?.count || 0), 0
                    );

                    const seller: SellerData = {
                        sellerId: sellerId || firstProduct._id || `seller-${sellerEmail}`,
                        name: firstProduct.companyName || sellerEmail.split('@')[0] || 'Seller',
                        email: sellerEmail,
                        companyName: firstProduct.companyName,
                        location: firstProduct.location,
                        phoneNumber: firstProduct.phoneNumber,
                        profilePhoto: firstProduct.images?.[0]?.url,
                        rating: {
                            average: avgRating,
                            count: totalReviews
                        },
                        products: sellerProducts,
                        productCount: sellerProducts.length,
                        isVerified: true,
                        joinedDate: firstProduct.createdAt || new Date().toISOString(),
                        totalSales,
                        description: categoryTitle
                            ? `Specializing in ${categoryTitle}. Quality products and excellent service.`
                            : 'Experienced seller offering quality products with great customer service.'
                    };

                    setSellerData(seller);
                } else {
                    // Create minimal seller data
                    const seller: SellerData = {
                        sellerId: sellerId || `seller-${sellerEmail}`,
                        name: sellerEmail.split('@')[0] || 'Seller',
                        email: sellerEmail,
                        location: 'Location not specified',
                        phoneNumber: 'Contact for details',
                        rating: { average: 0, count: 0 },
                        products: [],
                        productCount: 0,
                        isVerified: false,
                        joinedDate: new Date().toISOString(),
                        description: 'New seller on our platform.'
                    };

                    setSellerData(seller);
                }
            } else {
                throw new Error(response.message || 'Failed to fetch seller data');
            }
        } catch (err: any) {
            console.error('Error in useSellerData:', err);
            setError(err.message || 'Failed to load seller information');

            // Fallback data
            setSellerData({
                sellerId: sellerId || 'fallback-seller',
                name: sellerEmail.split('@')[0] || 'Seller',
                email: sellerEmail,
                location: 'Unknown',
                phoneNumber: 'N/A',
                rating: { average: 0, count: 0 },
                products: [],
                productCount: 0,
                isVerified: false,
                joinedDate: new Date().toISOString(),
                description: 'Seller information temporarily unavailable.'
            });
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, [sellerEmail, sellerId, categoryTitle]);

    useEffect(() => {
        fetchSellerData();
    }, [fetchSellerData]);

    const handleRefresh = useCallback(() => {
        fetchSellerData(true);
    }, [fetchSellerData]);

    return {
        sellerData,
        loading,
        error,
        refreshing,
        handleRefresh,
    };
}