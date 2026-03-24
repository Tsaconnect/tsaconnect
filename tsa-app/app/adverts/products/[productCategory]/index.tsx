import api from '@/components/services/api';
import { router, useLocalSearchParams } from 'expo-router';
import React, { useEffect, useState, useCallback } from 'react';
import {
    View,
    Text,
    StyleSheet,
    FlatList,
    Image,
    TouchableOpacity,
    ActivityIndicator,
    RefreshControl,
    Alert,
    TextInput,
    Pressable,
    ScrollView
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { SafeAreaView } from "react-native-safe-area-context"

interface Product {
    _id: string;
    name: string;
    description: string;
    price: number;
    discountedPrice?: number;
    images: { url: string; publicId: string }[];
    category: string;
    subcategory?: string;
    merchant: {
        _id: string;
        name: string;
        profilePhoto?: { url: string };
    };
    status: 'active' | 'inactive' | 'sold' | 'out_of_stock';
    type: 'Product' | 'Service';
    condition?: 'new' | 'used' | 'refurbished';
    stockQuantity?: number;
    createdAt: string;
    updatedAt: string;
}

const CategoryProductsScreen = () => {
    const { productCategory } = useLocalSearchParams<{ productCategory: string }>();

    const [products, setProducts] = useState<Product[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [searchQuery, setSearchQuery] = useState('');
    const [sortBy, setSortBy] = useState<string>('createdAt');
    const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('desc');
    const [minPrice, setMinPrice] = useState<number | undefined>();
    const [maxPrice, setMaxPrice] = useState<number | undefined>();
    const [productType, setProductType] = useState<'Product' | 'Service' | 'all'>('all');
    const [pagination, setPagination] = useState({
        page: 1,
        limit: 20,
        totalPages: 1,
        totalProducts: 0,
        hasNext: false,
        hasPrev: false
    });
    const [categoryDetails, setCategoryDetails] = useState<any>(null);
    const [filters, setFilters] = useState<any>(null);
    const [showFilters, setShowFilters] = useState(false);

    // Fetch products by category
    const fetchProducts = useCallback(async (page = 1, isRefreshing = false) => {
        try {
            if (!isRefreshing) setLoading(true);
            setError(null);

            const params: any = {
                categoryId: productCategory,
                page,
                limit: pagination.limit,
                sortBy,
                sortOrder,
                search: searchQuery || undefined
            };

            // Add optional filters
            if (minPrice !== undefined) params.minPrice = minPrice;
            if (maxPrice !== undefined) params.maxPrice = maxPrice;
            if (productType !== 'all') params.type = productType;

            const response = await api.getProductsByCategory(params);

            if (response.success && response.data) {
                if (page === 1 || isRefreshing) {
                    //@ts-ignore
                    setProducts(response.data.products);
                } else {
                    //@ts-ignore
                    setProducts(prev => [...prev, ...response.data.products]);
                }

                setCategoryDetails(response.data.category);
                setPagination({
                    page: response.data.pagination.page || page,
                    limit: response.data.pagination.limit || pagination.limit,
                    totalPages: response.data.pagination.totalPages || 1,
                    totalProducts: response.data.pagination.total || 0,
                    hasNext: response.data.pagination.hasNext || false,
                    hasPrev: response.data.pagination.hasPrev || false
                });
                setFilters(response.data.filters);
            } else {
                setError(response.message || 'Failed to fetch products');
            }
        } catch (error: any) {
            console.error('Error fetching products:', error);
            setError(error.message || 'An error occurred');
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, [productCategory, searchQuery, sortBy, sortOrder, minPrice, maxPrice, productType, pagination.limit]);

    // Initial fetch
    useEffect(() => {
        if (productCategory) {
            fetchProducts(1);
        }
    }, [productCategory, fetchProducts]);

    // Pull to refresh
    const onRefresh = useCallback(() => {
        setRefreshing(true);
        fetchProducts(1, true);
    }, [fetchProducts]);

    // Load more products
    const loadMore = () => {
        if (!loading && pagination.hasNext) {
            fetchProducts(pagination.page + 1);
        }
    };

    // Handle product press
    const handleProductPress = (productId: string) => {
        router.push(`/product/${productId}`);
    };

    // Apply filters
    const applyFilters = () => {
        setShowFilters(false);
        fetchProducts(1);
    };

    // Reset filters
    const resetFilters = () => {
        setMinPrice(undefined);
        setMaxPrice(undefined);
        setProductType('all');
        setSearchQuery('');
        setSortBy('createdAt');
        setSortOrder('desc');
        fetchProducts(1);
    };

    // Format price
    const formatPrice = (price: number) => {
        return new Intl.NumberFormat('en-NG', {
            style: 'currency',
            currency: 'NGN',
            minimumFractionDigits: 0,
            maximumFractionDigits: 0,
        }).format(price);
    };

    // Calculate discount percentage
    const calculateDiscount = (price: number, discountedPrice?: number) => {
        if (!discountedPrice) return 0;
        return Math.round(((price - discountedPrice) / price) * 100);
    };

    // Render product item
    const renderProductItem = ({ item }: { item: Product }) => (
        <TouchableOpacity
            style={styles.productCard}
            onPress={() => handleProductPress(item._id)}
            activeOpacity={0.7}
        >
            <View style={styles.productImageContainer}>
                {item.images && item.images.length > 0 ? (
                    <Image
                        source={{ uri: item.images[0].url }}
                        style={styles.productImage}
                        resizeMode="cover"
                    />
                ) : (
                    <View style={styles.placeholderImage}>
                        <Ionicons name="image-outline" size={40} color="#9CA3AF" />
                    </View>
                )}

                {item.discountedPrice && (
                    <View style={styles.discountBadge}>
                        <Text style={styles.discountText}>
                            -{calculateDiscount(item.price, item.discountedPrice)}%
                        </Text>
                    </View>
                )}
            </View>

            <View style={styles.productInfo}>
                <Text style={styles.productName} numberOfLines={2}>
                    {item.name}
                </Text>

                <Text style={styles.productDescription} numberOfLines={2}>
                    {item.description}
                </Text>

                <View style={styles.priceContainer}>
                    {item.discountedPrice ? (
                        <>
                            <Text style={styles.discountedPrice}>
                                {formatPrice(item.discountedPrice)}
                            </Text>
                            <Text style={styles.originalPrice}>
                                {formatPrice(item.price)}
                            </Text>
                        </>
                    ) : (
                        <Text style={styles.price}>
                            {formatPrice(item.price)}
                        </Text>
                    )}
                </View>

                <View style={styles.productMeta}>
                    <Text style={styles.productType}>
                        {item.type}
                    </Text>
                    <Text style={styles.productStatus}>
                        {item.status}
                    </Text>
                </View>

                <View style={styles.merchantInfo}>
                    {item.merchant?.profilePhoto?.url ? (
                        <Image
                            source={{ uri: item.merchant.profilePhoto.url }}
                            style={styles.merchantAvatar}
                        />
                    ) : (
                        <View style={styles.merchantAvatarPlaceholder}>
                            <Text style={styles.merchantInitial}>
                                {item.merchant?.name?.charAt(0).toUpperCase() || 'M'}
                            </Text>
                        </View>
                    )}
                    <Text style={styles.merchantName} numberOfLines={1}>
                        {item.merchant?.name || 'Unknown Merchant'}
                    </Text>
                </View>
            </View>
        </TouchableOpacity>
    );

    // Render filter modal
    const renderFilters = () => (
        <View style={styles.filterContainer}>
            <View style={styles.filterHeader}>
                <Text style={styles.filterTitle}>Filters</Text>
                <TouchableOpacity onPress={() => setShowFilters(false)}>
                    <Ionicons name="close" size={24} color="#374151" />
                </TouchableOpacity>
            </View>

            <ScrollView style={styles.filterContent}>
                <View style={styles.filterSection}>
                    <Text style={styles.filterSectionTitle}>Price Range</Text>
                    <View style={styles.priceInputs}>
                        <View style={styles.priceInput}>
                            <Text style={styles.priceLabel}>Min</Text>
                            <TextInput
                                style={styles.input}
                                placeholder="0"
                                keyboardType="numeric"
                                value={minPrice?.toString() || ''}
                                onChangeText={(text) => setMinPrice(text ? Number(text) : undefined)}
                            />
                        </View>
                        <View style={styles.priceInput}>
                            <Text style={styles.priceLabel}>Max</Text>
                            <TextInput
                                style={styles.input}
                                placeholder="1000000"
                                keyboardType="numeric"
                                value={maxPrice?.toString() || ''}
                                onChangeText={(text) => setMaxPrice(text ? Number(text) : undefined)}
                            />
                        </View>
                    </View>
                </View>

                <View style={styles.filterSection}>
                    <Text style={styles.filterSectionTitle}>Product Type</Text>
                    <View style={styles.typeOptions}>
                        <TouchableOpacity
                            style={[
                                styles.typeOption,
                                productType === 'all' && styles.typeOptionActive
                            ]}
                            onPress={() => setProductType('all')}
                        >
                            <Text style={[
                                styles.typeOptionText,
                                productType === 'all' && styles.typeOptionTextActive
                            ]}>All</Text>
                        </TouchableOpacity>
                        <TouchableOpacity
                            style={[
                                styles.typeOption,
                                productType === 'Product' && styles.typeOptionActive
                            ]}
                            onPress={() => setProductType('Product')}
                        >
                            <Text style={[
                                styles.typeOptionText,
                                productType === 'Product' && styles.typeOptionTextActive
                            ]}>Products</Text>
                        </TouchableOpacity>
                        <TouchableOpacity
                            style={[
                                styles.typeOption,
                                productType === 'Service' && styles.typeOptionActive
                            ]}
                            onPress={() => setProductType('Service')}
                        >
                            <Text style={[
                                styles.typeOptionText,
                                productType === 'Service' && styles.typeOptionTextActive
                            ]}>Services</Text>
                        </TouchableOpacity>
                    </View>
                </View>

                <View style={styles.filterSection}>
                    <Text style={styles.filterSectionTitle}>Sort By</Text>
                    <View style={styles.sortOptions}>
                        {[
                            { key: 'createdAt', label: 'Newest' },
                            { key: 'price', label: 'Price' },
                            { key: 'name', label: 'Name' }
                        ].map((option) => (
                            <TouchableOpacity
                                key={option.key}
                                style={[
                                    styles.sortOption,
                                    sortBy === option.key && styles.sortOptionActive
                                ]}
                                onPress={() => setSortBy(option.key)}
                            >
                                <Text style={[
                                    styles.sortOptionText,
                                    sortBy === option.key && styles.sortOptionTextActive
                                ]}>
                                    {option.label}
                                </Text>
                                {sortBy === option.key && (
                                    <TouchableOpacity onPress={() => setSortOrder(sortOrder === 'asc' ? 'desc' : 'asc')}>
                                        <Ionicons
                                            name={sortOrder === 'asc' ? 'arrow-up' : 'arrow-down'}
                                            size={16}
                                            color="#3B82F6"
                                        />
                                    </TouchableOpacity>
                                )}
                            </TouchableOpacity>
                        ))}
                    </View>
                </View>
            </ScrollView>

            <View style={styles.filterActions}>
                <TouchableOpacity style={styles.resetButton} onPress={resetFilters}>
                    <Text style={styles.resetButtonText}>Reset</Text>
                </TouchableOpacity>
                <TouchableOpacity style={styles.applyButton} onPress={applyFilters}>
                    <Text style={styles.applyButtonText}>Apply Filters</Text>
                </TouchableOpacity>
            </View>
        </View>
    );

    // Render loading state
    if (loading && products.length === 0) {
        return (
            <SafeAreaView style={styles.container}>
                <View style={styles.loadingContainer}>
                    <ActivityIndicator size="large" color="#3B82F6" />
                    <Text style={styles.loadingText}>Loading products...</Text>
                </View>
            </SafeAreaView>
        );
    }

    // Render error state
    if (error && products.length === 0) {
        return (
            <SafeAreaView style={styles.container}>
                <View style={styles.errorContainer}>
                    <Ionicons name="alert-circle-outline" size={64} color="#DC2626" />
                    <Text style={styles.errorTitle}>Failed to load products</Text>
                    <Text style={styles.errorMessage}>{error}</Text>
                    <TouchableOpacity style={styles.retryButton} onPress={() => fetchProducts(1)}>
                        <Text style={styles.retryButtonText}>Retry</Text>
                    </TouchableOpacity>
                </View>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={styles.container}>
            {/* Header */}
            <View style={styles.header}>
                <TouchableOpacity
                    style={styles.backButton}
                    onPress={() => router.back()}
                >
                    <Ionicons name="arrow-back" size={24} color="#374151" />
                </TouchableOpacity>

                <View style={styles.headerTitleContainer}>
                    <Text style={styles.headerTitle} numberOfLines={1}>
                        {categoryDetails?.name || productCategory || 'Products'}
                    </Text>
                    {pagination.totalProducts > 0 && (
                        <Text style={styles.productCount}>
                            {pagination.totalProducts} products
                        </Text>
                    )}
                </View>

                <TouchableOpacity
                    style={styles.filterButton}
                    onPress={() => setShowFilters(true)}
                >
                    <Ionicons name="filter-outline" size={24} color="#374151" />
                </TouchableOpacity>
            </View>

            {/* Search Bar */}
            <View style={styles.searchContainer}>
                <Ionicons name="search-outline" size={20} color="#9CA3AF" style={styles.searchIcon} />
                <TextInput
                    style={styles.searchInput}
                    placeholder="Search products..."
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    onSubmitEditing={() => fetchProducts(1)}
                    returnKeyType="search"
                />
                {searchQuery ? (
                    <TouchableOpacity onPress={() => setSearchQuery('')}>
                        <Ionicons name="close-circle" size={20} color="#9CA3AF" />
                    </TouchableOpacity>
                ) : null}
            </View>

            {/* Active Filters */}
            {(minPrice !== undefined || maxPrice !== undefined || productType !== 'all') && (
                <View style={styles.activeFilters}>
                    <Text style={styles.activeFiltersTitle}>Active Filters:</Text>
                    <ScrollView horizontal showsHorizontalScrollIndicator={false}>
                        {minPrice !== undefined && (
                            <View style={styles.filterChip}>
                                <Text style={styles.filterChipText}>Min: ${minPrice.toLocaleString()}</Text>
                            </View>
                        )}
                        {maxPrice !== undefined && (
                            <View style={styles.filterChip}>
                                <Text style={styles.filterChipText}>Max: ${maxPrice.toLocaleString()}</Text>
                            </View>
                        )}
                        {productType !== 'all' && (
                            <View style={styles.filterChip}>
                                <Text style={styles.filterChipText}>Type: {productType}</Text>
                            </View>
                        )}
                    </ScrollView>
                </View>
            )}

            {/* Products List */}
            <FlatList
                data={products}
                renderItem={renderProductItem}
                keyExtractor={(item) => item._id}
                numColumns={2}
                columnWrapperStyle={styles.columnWrapper}
                contentContainerStyle={styles.productsList}
                refreshControl={
                    <RefreshControl
                        refreshing={refreshing}
                        onRefresh={onRefresh}
                        colors={['#3B82F6']}
                        tintColor="#3B82F6"
                    />
                }
                onEndReached={loadMore}
                onEndReachedThreshold={0.5}
                ListEmptyComponent={
                    <View style={styles.emptyContainer}>
                        <Ionicons name="cube-outline" size={64} color="#9CA3AF" />
                        <Text style={styles.emptyTitle}>No products found</Text>
                        <Text style={styles.emptyMessage}>
                            {searchQuery
                                ? `No products found for "${searchQuery}"`
                                : `No products available in this category`}
                        </Text>
                        <TouchableOpacity
                            style={styles.exploreButton}
                            onPress={() => router.push('/home')}
                        >
                            <Text style={styles.exploreButtonText}>Explore Other Categories</Text>
                        </TouchableOpacity>
                    </View>
                }
                ListFooterComponent={
                    loading && products.length > 0 ? (
                        <View style={styles.footerLoading}>
                            <ActivityIndicator size="small" color="#3B82F6" />
                            <Text style={styles.footerLoadingText}>Loading more...</Text>
                        </View>
                    ) : null
                }
            />

            {/* Filters Modal */}
            {showFilters && (
                <View style={styles.modalOverlay}>
                    {renderFilters()}
                </View>
            )}
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#F9FAFB',
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
    },
    loadingText: {
        marginTop: 12,
        fontSize: 16,
        color: '#6B7280',
    },
    errorContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        padding: 20,
    },
    errorTitle: {
        fontSize: 20,
        fontWeight: '600',
        color: '#111827',
        marginTop: 16,
        marginBottom: 8,
    },
    errorMessage: {
        fontSize: 14,
        color: '#6B7280',
        textAlign: 'center',
        marginBottom: 24,
    },
    retryButton: {
        backgroundColor: '#3B82F6',
        paddingHorizontal: 24,
        paddingVertical: 12,
        borderRadius: 8,
    },
    retryButtonText: {
        color: '#FFFFFF',
        fontSize: 16,
        fontWeight: '600',
    },
    header: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: 16,
        paddingVertical: 12,
        backgroundColor: '#FFFFFF',
        borderBottomWidth: 1,
        borderBottomColor: '#E5E7EB',
    },
    backButton: {
        marginRight: 12,
    },
    headerTitleContainer: {
        flex: 1,
    },
    headerTitle: {
        fontSize: 18,
        fontWeight: '600',
        color: '#111827',
    },
    productCount: {
        fontSize: 12,
        color: '#6B7280',
        marginTop: 2,
    },
    filterButton: {
        padding: 4,
    },
    searchContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginHorizontal: 16,
        marginVertical: 12,
        paddingHorizontal: 12,
        backgroundColor: '#FFFFFF',
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#E5E7EB',
    },
    searchIcon: {
        marginRight: 8,
    },
    searchInput: {
        flex: 1,
        paddingVertical: 10,
        fontSize: 16,
        color: '#374151',
    },
    activeFilters: {
        paddingHorizontal: 16,
        marginBottom: 8,
    },
    activeFiltersTitle: {
        fontSize: 12,
        color: '#6B7280',
        marginBottom: 8,
    },
    filterChip: {
        backgroundColor: '#E0E7FF',
        paddingHorizontal: 12,
        paddingVertical: 6,
        borderRadius: 16,
        marginRight: 8,
    },
    filterChipText: {
        fontSize: 12,
        color: '#3730A3',
        fontWeight: '500',
    },
    productsList: {
        paddingHorizontal: 8,
        paddingBottom: 16,
    },
    columnWrapper: {
        justifyContent: 'space-between',
    },
    productCard: {
        flex: 1,
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        margin: 8,
        overflow: 'hidden',
        borderWidth: 1,
        borderColor: '#F3F4F6',
        maxWidth: '48%',
    },
    productImageContainer: {
        position: 'relative',
        width: '100%',
        aspectRatio: 1,
    },
    productImage: {
        width: '100%',
        height: '100%',
    },
    placeholderImage: {
        width: '100%',
        height: '100%',
        backgroundColor: '#F3F4F6',
        justifyContent: 'center',
        alignItems: 'center',
    },
    discountBadge: {
        position: 'absolute',
        top: 8,
        left: 8,
        backgroundColor: '#DC2626',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 4,
    },
    discountText: {
        color: '#FFFFFF',
        fontSize: 12,
        fontWeight: '600',
    },
    productInfo: {
        padding: 12,
    },
    productName: {
        fontSize: 14,
        fontWeight: '600',
        color: '#111827',
        marginBottom: 4,
    },
    productDescription: {
        fontSize: 12,
        color: '#6B7280',
        marginBottom: 8,
    },
    priceContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 8,
    },
    price: {
        fontSize: 16,
        fontWeight: '700',
        color: '#111827',
    },
    discountedPrice: {
        fontSize: 16,
        fontWeight: '700',
        color: '#DC2626',
        marginRight: 8,
    },
    originalPrice: {
        fontSize: 14,
        color: '#9CA3AF',
        textDecorationLine: 'line-through',
    },
    productMeta: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginBottom: 8,
    },
    productType: {
        fontSize: 10,
        color: '#3B82F6',
        backgroundColor: '#EFF6FF',
        paddingHorizontal: 6,
        paddingVertical: 2,
        borderRadius: 4,
        overflow: 'hidden',
    },
    productStatus: {
        fontSize: 10,
        color: '#059669',
        backgroundColor: '#D1FAE5',
        paddingHorizontal: 6,
        paddingVertical: 2,
        borderRadius: 4,
        overflow: 'hidden',
    },
    merchantInfo: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    merchantAvatar: {
        width: 24,
        height: 24,
        borderRadius: 12,
        marginRight: 8,
    },
    merchantAvatarPlaceholder: {
        width: 24,
        height: 24,
        borderRadius: 12,
        backgroundColor: '#3B82F6',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 8,
    },
    merchantInitial: {
        color: '#FFFFFF',
        fontSize: 12,
        fontWeight: '600',
    },
    merchantName: {
        fontSize: 12,
        color: '#6B7280',
        flex: 1,
    },
    emptyContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        paddingVertical: 64,
    },
    emptyTitle: {
        fontSize: 18,
        fontWeight: '600',
        color: '#111827',
        marginTop: 16,
        marginBottom: 8,
    },
    emptyMessage: {
        fontSize: 14,
        color: '#6B7280',
        textAlign: 'center',
        marginBottom: 24,
    },
    exploreButton: {
        backgroundColor: '#3B82F6',
        paddingHorizontal: 20,
        paddingVertical: 10,
        borderRadius: 8,
    },
    exploreButtonText: {
        color: '#FFFFFF',
        fontSize: 14,
        fontWeight: '600',
    },
    footerLoading: {
        flexDirection: 'row',
        justifyContent: 'center',
        alignItems: 'center',
        paddingVertical: 16,
    },
    footerLoadingText: {
        marginLeft: 8,
        fontSize: 14,
        color: '#6B7280',
    },
    modalOverlay: {
        position: 'absolute',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: 'rgba(0, 0, 0, 0.5)',
        justifyContent: 'flex-end',
    },
    filterContainer: {
        backgroundColor: '#FFFFFF',
        borderTopLeftRadius: 20,
        borderTopRightRadius: 20,
        maxHeight: '80%',
    },
    filterHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 20,
        paddingVertical: 16,
        borderBottomWidth: 1,
        borderBottomColor: '#E5E7EB',
    },
    filterTitle: {
        fontSize: 20,
        fontWeight: '600',
        color: '#111827',
    },
    filterContent: {
        paddingHorizontal: 20,
    },
    filterSection: {
        marginVertical: 16,
    },
    filterSectionTitle: {
        fontSize: 16,
        fontWeight: '600',
        color: '#374151',
        marginBottom: 12,
    },
    priceInputs: {
        flexDirection: 'row',
        justifyContent: 'space-between',
    },
    priceInput: {
        flex: 1,
        marginRight: 8,
    },
    priceLabel: {
        fontSize: 14,
        color: '#6B7280',
        marginBottom: 4,
    },
    input: {
        borderWidth: 1,
        borderColor: '#D1D5DB',
        borderRadius: 8,
        paddingHorizontal: 12,
        paddingVertical: 8,
        fontSize: 16,
        color: '#374151',
    },
    typeOptions: {
        flexDirection: 'row',
        flexWrap: 'wrap',
        gap: 8,
    },
    typeOption: {
        paddingHorizontal: 16,
        paddingVertical: 8,
        borderRadius: 20,
        backgroundColor: '#F3F4F6',
        borderWidth: 1,
        borderColor: '#F3F4F6',
    },
    typeOptionActive: {
        backgroundColor: '#E0E7FF',
        borderColor: '#3B82F6',
    },
    typeOptionText: {
        fontSize: 14,
        color: '#6B7280',
        fontWeight: '500',
    },
    typeOptionTextActive: {
        color: '#3B82F6',
    },
    sortOptions: {
        gap: 8,
    },
    sortOption: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 16,
        paddingVertical: 12,
        borderRadius: 8,
        backgroundColor: '#F3F4F6',
        borderWidth: 1,
        borderColor: '#F3F4F6',
    },
    sortOptionActive: {
        backgroundColor: '#E0E7FF',
        borderColor: '#3B82F6',
    },
    sortOptionText: {
        fontSize: 14,
        color: '#6B7280',
        fontWeight: '500',
    },
    sortOptionTextActive: {
        color: '#3B82F6',
    },
    filterActions: {
        flexDirection: 'row',
        paddingHorizontal: 20,
        paddingVertical: 16,
        borderTopWidth: 1,
        borderTopColor: '#E5E7EB',
        gap: 12,
    },
    resetButton: {
        flex: 1,
        paddingVertical: 12,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#D1D5DB',
        alignItems: 'center',
    },
    resetButtonText: {
        fontSize: 16,
        fontWeight: '600',
        color: '#374151',
    },
    applyButton: {
        flex: 1,
        paddingVertical: 12,
        borderRadius: 8,
        backgroundColor: '#3B82F6',
        alignItems: 'center',
    },
    applyButtonText: {
        fontSize: 16,
        fontWeight: '600',
        color: '#FFFFFF',
    },
});

export default CategoryProductsScreen;