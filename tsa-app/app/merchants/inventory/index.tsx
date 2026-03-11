import React, { useState, useEffect } from 'react';
import {
    View,
    Text,
    FlatList,
    StyleSheet,
    TextInput,
    TouchableOpacity,

    ActivityIndicator,
    ScrollView,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import api, { Product } from '@/components/services/api';
import { router } from 'expo-router';
import { SafeAreaView } from "react-native-safe-area-context"
interface ProductStats {
    totalProducts: number;
    featuredProducts: number;
    activeProducts: number;
    outOfStockProducts: number;
    totalSales: number;
    totalViews: number;
    averageRating: number;
}

export default function MerchantProductsScreen() {
    const [searchQuery, setSearchQuery] = useState('');
    const [activeTab, setActiveTab] = useState<'all' | 'featured' | 'non-featured'>('all');
    const [products, setProducts] = useState<Product[]>([]);
    const [stats, setStats] = useState<ProductStats | null>(null);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);

    function navigateToAddProduct() {
        router.replace('/merchants/inventory/add');
    }

    const fetchProducts = async () => {
        try {
            const response = await api.getMerchantProducts();

            if (response.success && response.data) {
                //@ts-ignore
                setProducts(response.data?.products);
            }
        } catch (error) {
            console.error('Error fetching products:', error);
        }
    };

    const fetchStats = async () => {
        try {
            // TODO: Add getProductStats method to API service
            // For now, stats are not implemented
            // When implemented, call: const response = await api.getProductStats();
            // Then check: if (response.success && response.data) { setStats(response.data); }
        } catch (error) {
            console.error('Error fetching stats:', error);
        }
    };

    const loadData = async () => {
        setLoading(true);
        await Promise.all([fetchProducts(), fetchStats()]);
        setLoading(false);
    };

    const onRefresh = async () => {
        setRefreshing(true);
        await loadData();
        setRefreshing(false);
    };

    useEffect(() => {
        loadData();
    }, []);

    const getFilteredProducts = () => {
        let filtered = products;

        // Apply tab filter
        if (activeTab === 'featured') {
            filtered = filtered.filter(product => product.isFeatured === true);
        } else if (activeTab === 'non-featured') {
            filtered = filtered.filter(product => product.isFeatured !== true);
        }

        // Apply search filter
        if (searchQuery.trim()) {
            const query = searchQuery.toLowerCase();
            filtered = filtered.filter(product =>
                product.name.toLowerCase().includes(query) ||
                product.categoryName?.toLowerCase().includes(query) ||
                product.description.toLowerCase().includes(query)
            );
        }

        return filtered;
    };

    const toggleFeatured = async (productId: string, currentStatus?: boolean) => {
        try {
            // TODO: Add toggleFeatured method to API service
            const response = { success: false, message: 'Toggle featured not implemented yet' };
            if (response.success) {
                // Update local state
                setProducts(prev =>
                    prev.map(product =>
                        product._id === productId
                            ? { ...product, isFeatured: !currentStatus }
                            : product
                    )
                );
            }
        } catch (error) {
            console.error('Error toggling featured:', error);
        }
    };

    const renderProductItem = ({ item }: { item: Product }) => (
        <View style={styles.itemCard}>
            <View style={styles.itemImagePlaceholder}>
                {item.images && item.images.length > 0 ? (
                    <View style={styles.imageContainer}>
                        <Feather name="image" size={24} color="#9b795fff" />
                    </View>
                ) : (
                    <Feather name="package" size={24} color="#9b795fff" />
                )}
                {item.isFeatured && (
                    <View style={styles.featuredBadge}>
                        <Feather name="star" size={12} color="white" />
                    </View>
                )}
            </View>
            <View style={styles.itemDetails}>
                <View style={styles.itemHeader}>
                    <View style={styles.titleContainer}>
                        <Text style={styles.itemName} numberOfLines={1}>{item.name}</Text>
                        {item.isFeatured && (
                            <View style={styles.featuredLabel}>
                                <Text style={styles.featuredLabelText}>Featured</Text>
                            </View>
                        )}
                    </View>
                    <TouchableOpacity
                        hitSlop={10}
                        onPress={() => toggleFeatured(item._id, item.isFeatured)}
                    >
                        {item.isFeatured ? (
                            <Feather name="star" size={20} color="#FFD700" />
                        ) : (
                            <Feather name="star" size={20} color="#666" />
                        )}
                    </TouchableOpacity>
                </View>
                <Text style={styles.itemCategory}>{item.categoryName || 'Uncategorized'}</Text>
                <Text style={styles.itemDescription} numberOfLines={2}>{item.description}</Text>
                <View style={styles.itemStats}>
                    <View style={styles.statItem}>
                        <Feather name="eye" size={14} color="#666" />
                        <Text style={styles.statText}>{item.views}</Text>
                    </View>
                    <View style={styles.statItem}>
                        <Feather name="shopping-bag" size={14} color="#666" />
                        <Text style={styles.statText}>{item.sales}</Text>
                    </View>
                    <View style={styles.statItem}>
                        <Feather name="star" size={14} color="#666" />
                        <Text style={styles.statText}>{item.rating.average.toFixed(1)}</Text>
                    </View>
                </View>
                <View style={styles.itemFooter}>
                    <Text style={styles.itemPrice}>${item.price.toFixed(2)}</Text>
                    <View style={[styles.stockBadge, item.stock === 0 && styles.outOfStockBadge]}>
                        <Text style={[styles.stockText, item.stock === 0 && styles.outOfStockText]}>
                            {item.stock === 0 ? 'Out of Stock' : `${item.stock} in stock`}
                        </Text>
                    </View>
                </View>
            </View>
        </View>
    );

    const renderStatsCard = () => {
        if (!stats) return null;

        return (
            <ScrollView
                horizontal
                showsHorizontalScrollIndicator={false}
                contentContainerStyle={styles.statsContainer}
            >
                <View style={styles.statCard}>
                    <Text style={styles.statNumber}>{stats.totalProducts}</Text>
                    <Text style={styles.statLabel}>Total Products</Text>
                </View>
                <View style={styles.statCard}>
                    <Text style={styles.statNumber}>{stats.featuredProducts}</Text>
                    <Text style={styles.statLabel}>Featured</Text>
                </View>
                <View style={styles.statCard}>
                    <Text style={styles.statNumber}>{stats.activeProducts}</Text>
                    <Text style={styles.statLabel}>Active</Text>
                </View>
                <View style={styles.statCard}>
                    <Text style={styles.statNumber}>{stats.outOfStockProducts}</Text>
                    <Text style={styles.statLabel}>Out of Stock</Text>
                </View>
                <View style={styles.statCard}>
                    <Text style={styles.statNumber}>{stats.totalSales}</Text>
                    <Text style={styles.statLabel}>Total Sales</Text>
                </View>
            </ScrollView>
        );
    };

    if (loading && !refreshing) {
        return (
            <View style={styles.loadingContainer}>
                <ActivityIndicator size="large" color="#007AFF" />
                <Text style={styles.loadingText}>Loading products...</Text>
            </View>
        );
    }

    const filteredProducts = getFilteredProducts();

    return (
        <SafeAreaView style={styles.container}>
            <View style={styles.header}>
                <Text style={styles.title}>My Products</Text>
                <TouchableOpacity onPress={navigateToAddProduct} style={styles.addButton}>
                    <Feather name="plus" size={24} color="white" />
                </TouchableOpacity>
            </View>

            {renderStatsCard()}

            <View style={styles.tabContainer}>
                <TouchableOpacity
                    style={[styles.tab, activeTab === 'all' && styles.activeTab]}
                    onPress={() => setActiveTab('all')}
                >
                    <Text style={[styles.tabText, activeTab === 'all' && styles.activeTabText]}>
                        All ({products.length})
                    </Text>
                </TouchableOpacity>
                <TouchableOpacity
                    style={[styles.tab, activeTab === 'featured' && styles.activeTab]}
                    onPress={() => setActiveTab('featured')}
                >
                    <Text style={[styles.tabText, activeTab === 'featured' && styles.activeTabText]}>
                        Featured ({products.filter(p => p.isFeatured === true).length})
                    </Text>
                </TouchableOpacity>
                <TouchableOpacity
                    style={[styles.tab, activeTab === 'non-featured' && styles.activeTab]}
                    onPress={() => setActiveTab('non-featured')}
                >
                    <Text style={[styles.tabText, activeTab === 'non-featured' && styles.activeTabText]}>
                        Not Featured ({products.filter(p => p.isFeatured !== true).length})
                    </Text>
                </TouchableOpacity>
            </View>

            <View style={styles.searchContainer}>
                <Feather name="search" size={20} color="#999" style={styles.searchIcon} />
                <TextInput
                    style={styles.searchInput}
                    placeholder="Search products..."
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    placeholderTextColor="#999"
                />
            </View>

            <FlatList
                data={filteredProducts}
                keyExtractor={(item) => item._id}
                renderItem={renderProductItem}
                contentContainerStyle={styles.listContent}
                showsVerticalScrollIndicator={false}
                refreshing={refreshing}
                onRefresh={onRefresh}
                ListEmptyComponent={
                    <View style={styles.emptyState}>
                        <Feather name="package" size={48} color="#ccc" />
                        <Text style={styles.emptyTitle}>No products found</Text>
                        <Text style={styles.emptyText}>
                            {searchQuery.trim() ? 'Try a different search term' : 'Create your first product'}
                        </Text>
                        {!searchQuery.trim() && (
                            <TouchableOpacity style={styles.createButton}>
                                <Text style={styles.createButtonText}>Create Product</Text>
                            </TouchableOpacity>
                        )}
                    </View>
                }
            />
        </SafeAreaView>
    );
}

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#f8f9fa',
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#f8f9fa',
    },
    loadingText: {
        marginTop: 12,
        fontSize: 16,
        color: '#666',
    },
    header: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 20,
        paddingVertical: 16,
    },
    title: {
        fontSize: 28,
        fontWeight: 'bold',
        color: '#1a1a1a',
    },
    addButton: {
        backgroundColor: '#9b795fff',
        width: 44,
        height: 44,
        borderRadius: 22,
        justifyContent: 'center',
        alignItems: 'center',
        elevation: 3,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
    },
    statsContainer: {
        paddingHorizontal: 20,
        paddingVertical: 12,
    },
    statCard: {
        backgroundColor: 'white',
        borderRadius: 12,
        padding: 16,
        marginRight: 12,
        alignItems: 'center',
        minWidth: 100,
        elevation: 2,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 1 },
        shadowOpacity: 0.05,
        shadowRadius: 3,
    },
    statNumber: {
        fontSize: 24,
        fontWeight: 'bold',
        color: '#1a1a1a',
        marginBottom: 4,
    },
    statLabel: {
        fontSize: 12,
        color: '#666',
        textAlign: 'center',
    },
    tabContainer: {
        flexDirection: 'row',
        paddingHorizontal: 20,
        paddingVertical: 12,
        backgroundColor: 'white',
        borderBottomWidth: 1,
        borderBottomColor: '#f0f0f0',
    },
    tab: {
        paddingHorizontal: 16,
        paddingVertical: 8,
        marginRight: 12,
        borderRadius: 20,
        backgroundColor: '#f5f5f5',
    },
    activeTab: {
        backgroundColor: '#c8aa77ff',
    },
    tabText: {
        fontSize: 14,
        color: '#666',
        fontWeight: '600',
    },
    activeTabText: {
        color: 'white',
    },
    searchContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'white',
        marginHorizontal: 20,
        marginVertical: 16,
        paddingHorizontal: 12,
        borderRadius: 12,
        borderWidth: 1,
        borderColor: '#e1e4e8',
    },
    searchIcon: {
        marginRight: 8,
    },
    searchInput: {
        flex: 1,
        height: 44,
        fontSize: 16,
        color: '#1a1a1a',
    },
    listContent: {
        paddingHorizontal: 20,
        paddingBottom: 20,
    },
    itemCard: {
        flexDirection: 'row',
        backgroundColor: 'white',
        borderRadius: 12,
        padding: 12,
        marginBottom: 12,
        borderWidth: 1,
        borderColor: '#f0f0f0',
    },
    itemImagePlaceholder: {
        width: 70,
        height: 70,
        borderRadius: 8,
        backgroundColor: '#f0f7ff',
        justifyContent: 'center',
        alignItems: 'center',
    },
    imageContainer: {
        width: '100%',
        height: '100%',
        justifyContent: 'center',
        alignItems: 'center',
    },
    featuredBadge: {
        position: 'absolute',
        top: -6,
        right: -6,
        backgroundColor: '#FFD700',
        width: 24,
        height: 24,
        borderRadius: 12,
        justifyContent: 'center',
        alignItems: 'center',
        borderWidth: 2,
        borderColor: 'white',
    },
    itemDetails: {
        flex: 1,
        marginLeft: 12,
    },
    itemHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'flex-start',
    },
    titleContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        flex: 1,
        marginRight: 8,
    },
    itemName: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1a1a1a',
        flex: 1,
    },
    featuredLabel: {
        backgroundColor: '#FFD70020',
        paddingHorizontal: 8,
        paddingVertical: 2,
        borderRadius: 4,
        marginLeft: 8,
    },
    featuredLabelText: {
        fontSize: 10,
        color: '#B8860B',
        fontWeight: '600',
    },
    itemCategory: {
        fontSize: 12,
        color: '#666',
        marginTop: 2,
    },
    itemDescription: {
        fontSize: 13,
        color: '#666',
        marginTop: 4,
        lineHeight: 18,
    },
    itemStats: {
        flexDirection: 'row',
        marginTop: 8,
        gap: 16,
    },
    statItem: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    statText: {
        fontSize: 12,
        color: '#666',
        marginLeft: 4,
    },
    itemFooter: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginTop: 10,
    },
    itemPrice: {
        fontSize: 16,
        fontWeight: '700',
        color: '#1a1a1a',
    },
    stockBadge: {
        backgroundColor: '#e6f4ea',
        paddingHorizontal: 10,
        paddingVertical: 4,
        borderRadius: 6,
    },
    outOfStockBadge: {
        backgroundColor: '#fde8e8',
    },
    stockText: {
        fontSize: 12,
        color: '#1e7e34',
        fontWeight: '600',
    },
    outOfStockText: {
        color: '#d62d20',
    },
    emptyState: {
        marginTop: 60,
        alignItems: 'center',
        paddingHorizontal: 40,
    },
    emptyTitle: {
        fontSize: 18,
        fontWeight: '600',
        color: '#666',
        marginTop: 16,
    },
    emptyText: {
        fontSize: 14,
        color: '#999',
        textAlign: 'center',
        marginTop: 8,
        lineHeight: 20,
    },
    createButton: {
        backgroundColor: '#98807eff',
        paddingHorizontal: 24,
        paddingVertical: 12,
        borderRadius: 8,
        marginTop: 20,
    },
    createButtonText: {
        color: 'white',
        fontSize: 16,
        fontWeight: '600',
    },
});