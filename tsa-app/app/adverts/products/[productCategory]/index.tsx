// app/adverts/products/[productCategory]/index.tsx
//
// Adverts — products in a category. Browse / search / sort / price + type filter
// the products of a single category, paginated with pull-to-refresh and
// infinite scroll. Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives). Data fetching (api.getProductsByCategory),
// pagination state, search/sort/price/type filters, currency formatting, and all
// navigation routes + params are unchanged — this is a visual / structural
// migration only.
import api from '@/components/services/api';
import { Stack, router, useLocalSearchParams } from 'expo-router';
import React, { useEffect, useState, useCallback } from 'react';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import { PromoPrice } from '@/components/promotion/PromoPrice';
import { isPromoted } from '@/utils/promo';
import {
    View,
    FlatList,
    Image,
    RefreshControl,
    ScrollView,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
    Badge,
    Button,
    Chip,
    Divider,
    EmptyState,
    ErrorState,
    Icon,
    IconButton,
    Input,
    PressableScale,
    Sheet,
    Spinner,
    Surface,
    Text,
} from '@/components/ui';

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
    const { colors, spacing, radius } = useTheme();

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

    const { currency, rate } = useCurrency();

    // Format price in selected currency
    const formatPrice = (usdPrice: number) =>
        formatPriceUtil(usdPrice, { currency, rate, minDecimals: 0, maxDecimals: 0 });

    // Calculate discount percentage
    const calculateDiscount = (price: number, discountedPrice?: number) => {
        if (!discountedPrice) return 0;
        return Math.round(((price - discountedPrice) / price) * 100);
    };

    // Render product item
    const renderProductItem = ({ item }: { item: Product }) => {
        const statusTone =
            item.status === 'active'
                ? colors.success
                : item.status === 'out_of_stock' || item.status === 'sold'
                    ? colors.danger
                    : colors.textMuted;
        const statusBg =
            item.status === 'active'
                ? colors.successBg
                : item.status === 'out_of_stock' || item.status === 'sold'
                    ? colors.dangerBg
                    : colors.surfaceAlt;

        return (
            <PressableScale
                onPress={() => handleProductPress(item._id)}
                style={{ flex: 1, maxWidth: '48%' }}
            >
                <Surface elevation="sm" style={{ overflow: 'hidden' }}>
                    <View style={{ position: 'relative', width: '100%', aspectRatio: 1 }}>
                        {item.images && item.images.length > 0 ? (
                            <Image
                                source={{ uri: item.images[0].url }}
                                style={{ width: '100%', height: '100%' }}
                                resizeMode="cover"
                            />
                        ) : (
                            <View
                                style={{
                                    width: '100%',
                                    height: '100%',
                                    backgroundColor: colors.surfaceAlt,
                                    justifyContent: 'center',
                                    alignItems: 'center',
                                }}
                            >
                                <Icon name="image" size={40} color={colors.textMuted} />
                            </View>
                        )}

                        {item.discountedPrice && (
                            <View style={{ position: 'absolute', top: spacing.sm, left: spacing.sm }}>
                                <Badge
                                    label={`-${calculateDiscount(item.price, item.discountedPrice)}%`}
                                    color={colors.onAccent}
                                    background={colors.danger}
                                    size="sm"
                                />
                            </View>
                        )}
                    </View>

                    <View style={{ padding: spacing.md }}>
                        <Text variant="bodySm" weight="700" numberOfLines={2}>
                            {item.name}
                        </Text>

                        <Text
                            variant="caption"
                            color="textSecondary"
                            numberOfLines={2}
                            style={{ marginTop: spacing.xs }}
                        >
                            {item.description}
                        </Text>

                        <View
                            style={{
                                flexDirection: 'row',
                                alignItems: 'baseline',
                                gap: spacing.xs,
                                marginTop: spacing.sm,
                            }}
                        >
                            {isPromoted(item) ? (
                                <PromoPrice product={item} format={formatPrice} />
                            ) : item.discountedPrice ? (
                                <>
                                    <Text variant="bodyMed" weight="700" color="danger" numeric>
                                        {formatPrice(item.discountedPrice)}
                                    </Text>
                                    <Text
                                        variant="caption"
                                        color="textMuted"
                                        numeric
                                        style={{ textDecorationLine: 'line-through' }}
                                    >
                                        {formatPrice(item.price)}
                                    </Text>
                                </>
                            ) : (
                                <Text variant="bodyMed" weight="700" numeric>
                                    {formatPrice(item.price)}
                                </Text>
                            )}
                        </View>

                        <View
                            style={{
                                flexDirection: 'row',
                                justifyContent: 'space-between',
                                alignItems: 'center',
                                marginTop: spacing.sm,
                            }}
                        >
                            <Badge
                                label={item.type}
                                color={colors.accent}
                                background={colors.accentMuted}
                                size="sm"
                            />
                            <Badge
                                label={item.status}
                                color={statusTone}
                                background={statusBg}
                                size="sm"
                            />
                        </View>

                        <Divider spacing="sm" />

                        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                            {item.merchant?.profilePhoto?.url ? (
                                <Image
                                    source={{ uri: item.merchant.profilePhoto.url }}
                                    style={{ width: 24, height: 24, borderRadius: 12 }}
                                />
                            ) : (
                                <View
                                    style={{
                                        width: 24,
                                        height: 24,
                                        borderRadius: 12,
                                        backgroundColor: colors.accentMuted,
                                        justifyContent: 'center',
                                        alignItems: 'center',
                                    }}
                                >
                                    <Text variant="overline" weight="700" color="accent">
                                        {item.merchant?.name?.charAt(0).toUpperCase() || 'M'}
                                    </Text>
                                </View>
                            )}
                            <Text variant="caption" color="textSecondary" numberOfLines={1} style={{ flex: 1 }}>
                                {item.merchant?.name || 'Unknown Merchant'}
                            </Text>
                        </View>
                    </View>
                </Surface>
            </PressableScale>
        );
    };

    // Type filter options (preserves productType state values)
    const TYPE_OPTIONS: { key: 'all' | 'Product' | 'Service'; label: string }[] = [
        { key: 'all', label: 'All' },
        { key: 'Product', label: 'Products' },
        { key: 'Service', label: 'Services' },
    ];

    // Sort options (preserves sortBy keys)
    const SORT_OPTIONS = [
        { key: 'createdAt', label: 'Newest' },
        { key: 'price', label: 'Price' },
        { key: 'name', label: 'Name' },
    ];

    // Render filter sheet
    const renderFilters = () => (
        <Sheet visible={showFilters} onClose={() => setShowFilters(false)} title="Filters">
            <ScrollView
                style={{ maxHeight: '70%' as any }}
                contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md }}
            >
                {/* Price Range */}
                <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.md }}>
                    Price Range
                </Text>
                <View style={{ flexDirection: 'row', gap: spacing.md }}>
                    <Input
                        containerStyle={{ flex: 1 }}
                        label="Min"
                        placeholder="0"
                        keyboardType="numeric"
                        value={minPrice?.toString() || ''}
                        onChangeText={(text) => setMinPrice(text ? Number(text) : undefined)}
                    />
                    <Input
                        containerStyle={{ flex: 1 }}
                        label="Max"
                        placeholder="1000000"
                        keyboardType="numeric"
                        value={maxPrice?.toString() || ''}
                        onChangeText={(text) => setMaxPrice(text ? Number(text) : undefined)}
                    />
                </View>

                {/* Product Type */}
                <Text variant="bodyMed" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
                    Product Type
                </Text>
                <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                    {TYPE_OPTIONS.map((option) => (
                        <Chip
                            key={option.key}
                            label={option.label}
                            selected={productType === option.key}
                            onPress={() => setProductType(option.key)}
                        />
                    ))}
                </View>

                {/* Sort By */}
                <Text variant="bodyMed" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
                    Sort By
                </Text>
                <View style={{ gap: spacing.sm }}>
                    {SORT_OPTIONS.map((option) => {
                        const active = sortBy === option.key;
                        return (
                            <PressableScale
                                key={option.key}
                                onPress={() => setSortBy(option.key)}
                                style={{
                                    flexDirection: 'row',
                                    justifyContent: 'space-between',
                                    alignItems: 'center',
                                    paddingHorizontal: spacing.lg,
                                    minHeight: 48,
                                    borderRadius: radius.md,
                                    borderWidth: 1,
                                    borderColor: active ? colors.accent : colors.border,
                                    backgroundColor: active ? colors.accentMuted : colors.surface,
                                }}
                            >
                                <Text
                                    variant="bodySm"
                                    weight={active ? '600' : '400'}
                                    color={active ? 'accent' : 'textSecondary'}
                                >
                                    {option.label}
                                </Text>
                                {active && (
                                    <IconButton
                                        icon={sortOrder === 'asc' ? 'arrowUp' : 'arrowDown'}
                                        accessibilityLabel="Toggle sort order"
                                        size={16}
                                        color={colors.accent}
                                        onPress={() => setSortOrder(sortOrder === 'asc' ? 'desc' : 'asc')}
                                    />
                                )}
                            </PressableScale>
                        );
                    })}
                </View>
            </ScrollView>

            <View style={{ flexDirection: 'row', gap: spacing.md, paddingHorizontal: spacing.lg, paddingTop: spacing.lg }}>
                <Button title="Reset" variant="secondary" onPress={resetFilters} style={{ flex: 1 }} />
                <Button title="Apply Filters" onPress={applyFilters} style={{ flex: 1 }} />
            </View>
        </Sheet>
    );

    // Render loading state
    if (loading && products.length === 0) {
        return (
            <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
                <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', gap: spacing.md }}>
                    <Spinner size="large" />
                    <Text variant="bodySm" color="textSecondary">
                        Loading products...
                    </Text>
                </View>
            </SafeAreaView>
        );
    }

    // Render error state
    if (error && products.length === 0) {
        return (
            <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
                <View style={{ flex: 1, justifyContent: 'center' }}>
                    <ErrorState
                        title="Failed to load products"
                        subtitle={error}
                        onRetry={() => fetchProducts(1)}
                        retryLabel="Retry"
                    />
                </View>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            {/* Title, back and the filter action all live in the native header
                now — app/adverts/_layout.tsx used to render an unstyled default
                header ON TOP of this row. */}
            <Stack.Screen
                options={{
                    title: categoryDetails?.name || productCategory || 'Products',
                    headerRight: () => (
                        <IconButton
                            icon="filter"
                            accessibilityLabel="Open filters"
                            color={colors.accent}
                            onPress={() => setShowFilters(true)}
                        />
                    ),
                }}
            />

            {/* Search Bar */}
            <View style={{ paddingHorizontal: spacing.lg, paddingVertical: spacing.md }}>
                <Input
                    placeholder="Search products..."
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    onSubmitEditing={() => fetchProducts(1)}
                    returnKeyType="search"
                    leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
                    rightAdornment={
                        searchQuery ? (
                            <PressableScale onPress={() => setSearchQuery('')} hitSlop={8}>
                                <Icon name="closeCircle" size={18} color={colors.textMuted} />
                            </PressableScale>
                        ) : undefined
                    }
                />
            </View>

            {/* Active Filters */}
            {(minPrice !== undefined || maxPrice !== undefined || productType !== 'all') && (
                <View style={{ paddingHorizontal: spacing.lg, marginBottom: spacing.sm }}>
                    <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.sm }}>
                        Active Filters:
                    </Text>
                    <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: spacing.sm }}>
                        {minPrice !== undefined && (
                            <Badge label={`Min: $${minPrice.toLocaleString()}`} color={colors.accent} background={colors.accentMuted} />
                        )}
                        {maxPrice !== undefined && (
                            <Badge label={`Max: $${maxPrice.toLocaleString()}`} color={colors.accent} background={colors.accentMuted} />
                        )}
                        {productType !== 'all' && (
                            <Badge label={`Type: ${productType}`} color={colors.accent} background={colors.accentMuted} />
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
                columnWrapperStyle={{ justifyContent: 'space-between', gap: spacing.md }}
                contentContainerStyle={{
                    paddingHorizontal: spacing.lg,
                    paddingBottom: spacing['4xl'],
                    gap: spacing.md,
                }}
                refreshControl={
                    <RefreshControl
                        refreshing={refreshing}
                        onRefresh={onRefresh}
                        tintColor={colors.accent}
                    />
                }
                onEndReached={loadMore}
                onEndReachedThreshold={0.5}
                ListEmptyComponent={
                    <EmptyState
                        icon="storefront"
                        title="No products found"
                        subtitle={
                            searchQuery
                                ? `No products found for "${searchQuery}"`
                                : `No products available in this category`
                        }
                        actionLabel="Explore Other Categories"
                        onAction={() => router.push('/home')}
                    />
                }
                ListFooterComponent={
                    loading && products.length > 0 ? (
                        <View style={{ flexDirection: 'row', justifyContent: 'center', alignItems: 'center', gap: spacing.sm, paddingVertical: spacing.lg }}>
                            <Spinner size="small" />
                            <Text variant="bodySm" color="textSecondary">
                                Loading more...
                            </Text>
                        </View>
                    ) : null
                }
            />

            {/* Filters Sheet */}
            {renderFilters()}
        </SafeAreaView>
    );
};

export default CategoryProductsScreen;
