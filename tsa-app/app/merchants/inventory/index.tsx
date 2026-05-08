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
    Switch,
    Alert,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import api, { Product } from '@/components/services/api';
import { router } from 'expo-router';
import { SafeAreaView } from "react-native-safe-area-context";
import { useAuth } from '@/AuthContext/AuthContext';

type ActiveTab = 'all' | 'featured' | 'non-featured';
type SectionTab = 'products' | 'services';

export default function MerchantInventoryScreen() {
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

        return (
            <TouchableOpacity style={styles.itemCard} activeOpacity={0.7} onPress={() => navigateToDetail(item)}>
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
                            <Text style={styles.statText}>{(item.rating?.average ?? 0).toFixed(1)}</Text>
                        </View>
                    </View>
                    <View style={styles.itemFooter}>
                        <Text style={styles.itemPrice}>${item.price.toFixed(2)}</Text>
                        <View style={styles.statusRow}>
                            <Text style={[styles.statusLabel, isActive ? styles.activeLabel : styles.inactiveLabel]}>
                                {isActive ? 'Active' : 'Inactive'}
                            </Text>
                            {toggling ? (
                                <ActivityIndicator size="small" color="#9b795fff" style={{ marginLeft: 6 }} />
                            ) : (
                                <Switch
                                    value={isActive}
                                    onValueChange={() => handleToggleStatus(item)}
                                    trackColor={{ false: '#ddd', true: '#a78bfa' }}
                                    thumbColor={isActive ? '#7c3aed' : '#999'}
                                    style={{ transform: [{ scaleX: 0.8 }, { scaleY: 0.8 }] }}
                                />
                            )}
                        </View>
                    </View>
                </View>
            </TouchableOpacity>
        );
    };

    if (loading && !refreshing) {
        return (
            <View style={styles.loadingContainer}>
                <ActivityIndicator size="large" color="#007AFF" />
                <Text style={styles.loadingText}>Loading inventory...</Text>
            </View>
        );
    }

    const myProducts = getFilteredItems('Product');
    const myServices = getFilteredItems('Service');
    const currentItems = sectionTab === 'products' ? myProducts : myServices;
    const allProducts = products.filter(p => p.type === 'Product');
    const allServices = products.filter(p => p.type === 'Service');

    return (
        <SafeAreaView style={styles.container}>
            {/* Section toggle */}
            <View style={styles.sectionToggle}>
                <TouchableOpacity
                    style={[styles.sectionBtn, sectionTab === 'products' && styles.sectionBtnActive]}
                    onPress={() => setSectionTab('products')}
                >
                    <Text style={[styles.sectionBtnText, sectionTab === 'products' && styles.sectionBtnTextActive]}>
                        My Products ({allProducts.length})
                    </Text>
                </TouchableOpacity>
                <TouchableOpacity
                    style={[styles.sectionBtn, sectionTab === 'services' && styles.sectionBtnActive]}
                    onPress={() => setSectionTab('services')}
                >
                    <Text style={[styles.sectionBtnText, sectionTab === 'services' && styles.sectionBtnTextActive]}>
                        My Services ({allServices.length})
                    </Text>
                </TouchableOpacity>
            </View>

            {/* Header with section title and + button */}
            <View style={styles.header}>
                <Text style={styles.title}>
                    {sectionTab === 'products' ? 'My Products' : 'My Services'}
                </Text>
                <TouchableOpacity
                    onPress={sectionTab === 'products' ? navigateToAddProduct : navigateToAddService}
                    style={styles.addButton}
                >
                    <Feather name="plus" size={24} color="white" />
                </TouchableOpacity>
            </View>

            {/* Feature tabs */}
            <View style={styles.tabContainer}>
                {(['all', 'featured', 'non-featured'] as ActiveTab[]).map(tab => {
                    const base = sectionTab === 'products' ? allProducts : allServices;
                    const count = tab === 'all' ? base.length
                        : tab === 'featured' ? base.filter(p => p.isFeatured === true).length
                        : base.filter(p => p.isFeatured !== true).length;
                    const label = tab === 'all' ? `All (${count})` : tab === 'featured' ? `Featured (${count})` : `Not Featured (${count})`;
                    return (
                        <TouchableOpacity
                            key={tab}
                            style={[styles.tab, activeTab === tab && styles.activeTab]}
                            onPress={() => setActiveTab(tab)}
                        >
                            <Text style={[styles.tabText, activeTab === tab && styles.activeTabText]}>
                                {label}
                            </Text>
                        </TouchableOpacity>
                    );
                })}
            </View>

            <View style={styles.searchContainer}>
                <Feather name="search" size={20} color="#999" style={styles.searchIcon} />
                <TextInput
                    style={styles.searchInput}
                    placeholder={`Search ${sectionTab === 'products' ? 'products' : 'services'}...`}
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    placeholderTextColor="#999"
                />
            </View>

            <FlatList
                data={currentItems}
                keyExtractor={item => item.id || item._id || Math.random().toString()}
                renderItem={renderItem}
                contentContainerStyle={styles.listContent}
                showsVerticalScrollIndicator={false}
                refreshing={refreshing}
                onRefresh={onRefresh}
                ListEmptyComponent={
                    <View style={styles.emptyState}>
                        <Feather name={sectionTab === 'products' ? 'package' : 'tool'} size={48} color="#ccc" />
                        <Text style={styles.emptyTitle}>
                            No {sectionTab === 'products' ? 'products' : 'services'} found
                        </Text>
                        <Text style={styles.emptyText}>
                            {searchQuery.trim()
                                ? 'Try a different search term'
                                : `Add your first ${sectionTab === 'products' ? 'product' : 'service'}`}
                        </Text>
                        {!searchQuery.trim() && (
                            <TouchableOpacity
                                style={styles.createButton}
                                onPress={sectionTab === 'products' ? navigateToAddProduct : navigateToAddService}
                            >
                                <Text style={styles.createButtonText}>
                                    {sectionTab === 'products' ? 'Add Product' : 'Add Service'}
                                </Text>
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
    sectionToggle: {
        flexDirection: 'row',
        paddingHorizontal: 20,
        paddingTop: 12,
        gap: 10,
    },
    sectionBtn: {
        flex: 1,
        paddingVertical: 10,
        borderRadius: 10,
        alignItems: 'center',
        backgroundColor: '#f0f0f0',
        borderWidth: 1.5,
        borderColor: 'transparent',
    },
    sectionBtnActive: {
        backgroundColor: '#fff',
        borderColor: '#c8aa77ff',
    },
    sectionBtnText: {
        fontSize: 13,
        fontWeight: '600',
        color: '#888',
    },
    sectionBtnTextActive: {
        color: '#9b795fff',
    },
    header: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 20,
        paddingVertical: 12,
    },
    title: {
        fontSize: 24,
        fontWeight: 'bold',
        color: '#1a1a1a',
    },
    addButton: {
        backgroundColor: '#9b795fff',
        width: 40,
        height: 40,
        borderRadius: 20,
        justifyContent: 'center',
        alignItems: 'center',
        elevation: 3,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
    },
    tabContainer: {
        flexDirection: 'row',
        paddingHorizontal: 20,
        paddingVertical: 10,
        backgroundColor: 'white',
        borderBottomWidth: 1,
        borderBottomColor: '#f0f0f0',
        gap: 8,
    },
    tab: {
        paddingHorizontal: 12,
        paddingVertical: 7,
        borderRadius: 20,
        backgroundColor: '#f5f5f5',
    },
    activeTab: {
        backgroundColor: '#c8aa77ff',
    },
    tabText: {
        fontSize: 13,
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
        marginVertical: 12,
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
        fontSize: 15,
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
        width: 22,
        height: 22,
        borderRadius: 11,
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
        fontSize: 15,
        fontWeight: '600',
        color: '#1a1a1a',
        flex: 1,
    },
    featuredLabel: {
        backgroundColor: '#FFD70020',
        paddingHorizontal: 6,
        paddingVertical: 2,
        borderRadius: 4,
        marginLeft: 6,
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
        marginTop: 6,
        gap: 14,
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
        marginTop: 8,
    },
    itemPrice: {
        fontSize: 15,
        fontWeight: '700',
        color: '#1a1a1a',
    },
    statusRow: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    statusLabel: {
        fontSize: 12,
        fontWeight: '600',
    },
    activeLabel: {
        color: '#7c3aed',
    },
    inactiveLabel: {
        color: '#9ca3af',
    },
    emptyState: {
        marginTop: 60,
        alignItems: 'center',
        paddingHorizontal: 40,
    },
    emptyTitle: {
        fontSize: 17,
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
        fontSize: 15,
        fontWeight: '600',
    },
});
