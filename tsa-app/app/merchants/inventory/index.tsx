// app/merchants/inventory/index.tsx
//
// Merchant inventory list — browse / search owned products & services, switch
// product/service sections, filter by featured state, and toggle each item's
// active/inactive status inline. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives). Data fetching
// (api.getMerchantProducts / api.toggleProductStatus), token wiring, state,
// effects, filtering, status-toggle flow, and all navigation routes + params
// are unchanged — this is a visual / structural migration only.
import React, { useState, useEffect } from 'react';
import { View, FlatList, RefreshControl, Alert } from 'react-native';
import api, { Product } from '@/components/services/api';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '@/AuthContext/AuthContext';
import { useTheme } from '@/theme';
import {
  AmountText,
  Badge,
  Chip,
  EmptyState,
  Icon,
  IconButton,
  Input,
  PressableScale,
  SegmentedControl,
  Spinner,
  Surface,
  Text,
  Toggle,
} from '@/components/ui';

type ActiveTab = 'all' | 'featured' | 'non-featured';
type SectionTab = 'products' | 'services';

export default function MerchantInventoryScreen() {
    const { colors, spacing, radius } = useTheme();
    const { token } = useAuth();
    const [searchQuery, setSearchQuery] = useState('');
    const [activeTab, setActiveTab] = useState<ActiveTab>('all');
    const [sectionTab, setSectionTab] = useState<SectionTab>('products');
    const [products, setProducts] = useState<Product[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [togglingId, setTogglingId] = useState<string | null>(null);

    const fetchProducts = async () => {
        try {
            const response = await api.getMerchantProducts();
            if (response.success && response.data) {
                //@ts-ignore
                setProducts(response.data?.products ?? []);
            }
        } catch (error) {
            console.error('Error fetching products:', error);
        }
    };

    const loadData = async () => {
        setLoading(true);
        await fetchProducts();
        setLoading(false);
    };

    const onRefresh = async () => {
        setRefreshing(true);
        await fetchProducts();
        setRefreshing(false);
    };

    useEffect(() => {
        if (token) {
            api.setToken(token.replace('Bearer ', ''));
            loadData();
        }
    }, [token]);

    const handleToggleStatus = async (item: Product) => {
        const id = item.id || item._id || '';
        if (!id) return;
        const newStatus = item.status === 'active' ? 'inactive' : 'active';
        setTogglingId(id);
        const response = await api.toggleProductStatus(id, newStatus);
        if (response.success) {
            setProducts(prev =>
                prev.map(p => (p.id || p._id) === id ? { ...p, status: newStatus } : p)
            );
        } else {
            Alert.alert('Error', response.message || 'Failed to update status');
        }
        setTogglingId(null);
    };

    const getFilteredItems = (type: 'Product' | 'Service') => {
        let filtered = products.filter(p => p.type === type);
        if (activeTab === 'featured') filtered = filtered.filter(p => p.isFeatured === true);
        else if (activeTab === 'non-featured') filtered = filtered.filter(p => p.isFeatured !== true);
        if (searchQuery.trim()) {
            const q = searchQuery.toLowerCase();
            filtered = filtered.filter(p =>
                p.name.toLowerCase().includes(q) ||
                p.categoryName?.toLowerCase().includes(q) ||
                p.description.toLowerCase().includes(q)
            );
        }
        return filtered;
    };

    const navigateToAddProduct = () => router.replace('/merchants/inventory/add');
    const navigateToAddService = () => router.push({ pathname: '/serviceaction', params: { index: 1 } } as any);
    const navigateToDetail = (item: Product) => {
        const id = item.id || item._id;
        router.push(`/merchants/inventory/${id}` as any);
    };

    const renderItem = ({ item }: { item: Product }) => {
        const id = item.id || item._id;
        const isActive = item.status === 'active';
        const toggling = togglingId === id;
        const hasImage = !!(item.images && item.images.length > 0);

        return (
            <PressableScale onPress={() => navigateToDetail(item)} style={{ marginBottom: spacing.md }}>
                <Surface elevation="sm" padding="md" style={{ flexDirection: 'row' }}>
                    <View
                        style={{
                            width: 70,
                            height: 70,
                            borderRadius: radius.md,
                            backgroundColor: colors.accentMuted,
                            alignItems: 'center',
                            justifyContent: 'center',
                        }}
                    >
                        <Icon name={hasImage ? 'image' : 'storefront'} size={24} color={colors.accent} />
                        {item.isFeatured && (
                            <View
                                style={{
                                    position: 'absolute',
                                    top: -6,
                                    right: -6,
                                    width: 22,
                                    height: 22,
                                    borderRadius: radius.pill,
                                    backgroundColor: colors.accent,
                                    alignItems: 'center',
                                    justifyContent: 'center',
                                    borderWidth: 2,
                                    borderColor: colors.surface,
                                }}
                            >
                                <Icon name="star" size={12} color={colors.onAccent} />
                            </View>
                        )}
                    </View>

                    <View style={{ flex: 1, marginLeft: spacing.md }}>
                        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                            <Text variant="bodySm" weight="700" numberOfLines={1} style={{ flex: 1 }}>
                                {item.name}
                            </Text>
                            {item.isFeatured && (
                                <Badge label="Featured" icon="star" size="sm" color={colors.accent} background={colors.accentMuted} />
                            )}
                        </View>

                        <Text variant="caption" color="textSecondary" style={{ marginTop: 2 }}>
                            {item.categoryName || 'Uncategorized'}
                        </Text>
                        <Text variant="caption" color="textSecondary" numberOfLines={2} style={{ marginTop: spacing.xs }}>
                            {item.description}
                        </Text>

                        <View style={{ flexDirection: 'row', gap: spacing.lg, marginTop: spacing.sm }}>
                            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                                <Icon name="eye" size={14} color={colors.textMuted} />
                                <Text variant="caption" color="textSecondary">{item.views}</Text>
                            </View>
                            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                                <Icon name="cart" size={14} color={colors.textMuted} />
                                <Text variant="caption" color="textSecondary">{item.sales}</Text>
                            </View>
                            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                                <Icon name="star" size={14} color={colors.textMuted} />
                                <Text variant="caption" color="textSecondary">{(item.rating?.average ?? 0).toFixed(1)}</Text>
                            </View>
                        </View>

                        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginTop: spacing.sm }}>
                            <AmountText value={item.price} currency="USD" variant="bodySm" minimumFractionDigits={2} maximumFractionDigits={2} />
                            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                                <Text variant="caption" weight="600" color={isActive ? 'accent' : 'textMuted'}>
                                    {isActive ? 'Active' : 'Inactive'}
                                </Text>
                                {toggling ? (
                                    <Spinner size="small" />
                                ) : (
                                    <Toggle
                                        value={isActive}
                                        onValueChange={() => handleToggleStatus(item)}
                                        accessibilityLabel={`Toggle ${item.name} status`}
                                    />
                                )}
                            </View>
                        </View>
                    </View>
                </Surface>
            </PressableScale>
        );
    };

    if (loading && !refreshing) {
        return (
            <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
                <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', gap: spacing.md }}>
                    <Spinner size="large" />
                    <Text variant="bodySm" color="textSecondary">Loading inventory...</Text>
                </View>
            </SafeAreaView>
        );
    }

    const myProducts = getFilteredItems('Product');
    const myServices = getFilteredItems('Service');
    const currentItems = sectionTab === 'products' ? myProducts : myServices;
    const allProducts = products.filter(p => p.type === 'Product');
    const allServices = products.filter(p => p.type === 'Service');

    const base = sectionTab === 'products' ? allProducts : allServices;
    const tabCount = (tab: ActiveTab) =>
        tab === 'all' ? base.length
        : tab === 'featured' ? base.filter(p => p.isFeatured === true).length
        : base.filter(p => p.isFeatured !== true).length;

    return (
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
                {/* Section toggle */}
                <SegmentedControl
                    segments={[
                        { key: 'products', label: `My Products (${allProducts.length})` },
                        { key: 'services', label: `My Services (${allServices.length})` },
                    ]}
                    value={sectionTab}
                    onChange={(k) => setSectionTab(k as SectionTab)}
                />

                {/* Header with section title and + button */}
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
                    <Text variant="title1" weight="700">
                        {sectionTab === 'products' ? 'My Products' : 'My Services'}
                    </Text>
                    <IconButton
                        icon="plus"
                        variant="surface"
                        color={colors.accent}
                        onPress={sectionTab === 'products' ? navigateToAddProduct : navigateToAddService}
                        accessibilityLabel={sectionTab === 'products' ? 'Add product' : 'Add service'}
                    />
                </View>

                {/* Feature tabs */}
                <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                    {(['all', 'featured', 'non-featured'] as ActiveTab[]).map(tab => {
                        const count = tabCount(tab);
                        const label = tab === 'all' ? `All (${count})` : tab === 'featured' ? `Featured (${count})` : `Not Featured (${count})`;
                        return (
                            <Chip
                                key={tab}
                                label={label}
                                selected={activeTab === tab}
                                onPress={() => setActiveTab(tab)}
                            />
                        );
                    })}
                </View>

                <Input
                    placeholder={`Search ${sectionTab === 'products' ? 'products' : 'services'}...`}
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
                    rightAdornment={
                        searchQuery.length > 0 ? (
                            <PressableScale onPress={() => setSearchQuery('')} hitSlop={8}>
                                <Icon name="close" size={16} color={colors.textMuted} />
                            </PressableScale>
                        ) : undefined
                    }
                />
            </View>

            <FlatList
                data={currentItems}
                keyExtractor={item => item.id || item._id || Math.random().toString()}
                renderItem={renderItem}
                contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing['4xl'] }}
                showsVerticalScrollIndicator={false}
                refreshControl={
                    <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
                }
                ListEmptyComponent={
                    <EmptyState
                        icon={sectionTab === 'products' ? 'storefront' : 'construction'}
                        title={`No ${sectionTab === 'products' ? 'products' : 'services'} found`}
                        subtitle={
                            searchQuery.trim()
                                ? 'Try a different search term'
                                : `Add your first ${sectionTab === 'products' ? 'product' : 'service'}`
                        }
                        actionLabel={
                            !searchQuery.trim()
                                ? (sectionTab === 'products' ? 'Add Product' : 'Add Service')
                                : undefined
                        }
                        onAction={
                            !searchQuery.trim()
                                ? (sectionTab === 'products' ? navigateToAddProduct : navigateToAddService)
                                : undefined
                        }
                    />
                }
            />
        </SafeAreaView>
    );
}
