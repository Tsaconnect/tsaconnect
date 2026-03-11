import React, { useState, useEffect } from 'react';
import {
    View,
    Text,
    FlatList,
    StyleSheet,
    TextInput,
    TouchableOpacity,
    ActivityIndicator,
    Alert,
    Dimensions,
} from 'react-native';
import { Feather, Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { router } from 'expo-router';
import api, { Product } from '@/components/services/api';
import { SafeAreaView } from "react-native-safe-area-context"
const { width } = Dimensions.get('window');

// Color palette with brown as primary
const COLORS = {
    primary: '#8B4513', // Saddle Brown
    primaryLight: '#A0522D', // Sienna
    primaryDark: '#654321', // Dark Brown
    secondary: '#D2691E', // Chocolate
    accent: '#CD853F', // Peru
    background: '#FAF3E0', // Soft cream background
    card: '#FFFFFF',
    text: '#2C1810',
    textLight: '#7A5C3C',
    textMuted: '#A1887F',
    success: '#2E8B57', // Sea Green
    error: '#DC2626',
    warning: '#F59E0B',
    border: '#E0D5C4',
    shimmer: '#F5F5F5',
};

export default function AdminAdvertRequestsScreen() {
    const [searchQuery, setSearchQuery] = useState('');
    const [products, setProducts] = useState<Product[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [approvingId, setApprovingId] = useState<string | null>(null);
    const [rejectingId, setRejectingId] = useState<string | null>(null);
    const [token, setToken] = useState<string | null>(null);

    // Get token on component mount
    useEffect(() => {
        const getToken = async () => {
            try {
                const storedToken = await api.getStoredToken();
                setToken(storedToken);
                if (!storedToken) {
                    Alert.alert("Session Expired", "Please login again.");
                    router.replace("/login");
                }
            } catch (error) {
                console.error("Error getting token:", error);
            }
        };
        getToken();
    }, []);

    const fetchRequests = async () => {
        try {
            const currentToken = await api.getStoredToken();
            if (!currentToken) {
                Alert.alert("Session Expired", "Please login again.");
                router.replace("/login");
                return;
            }

            const response = await api.getNonFeaturedProducts();
            console.log('API Response:', response);

            if (response.success && response.data) {
                // Handle both response structures
                if (Array.isArray(response.data)) {
                    setProducts(response.data);
                } else if (response.data && Array.isArray(response.data)) {
                    setProducts(response.data);
                } else {
                    setProducts([]);
                }
            } else if (response.message === "Please authenticate") {
                Alert.alert("Session Expired", "Please login again.");
                router.replace("/login");
            }
        } catch (error: any) {
            console.error('Error fetching advert requests:', error);
            if (error.message?.includes("authenticate") || error.response?.status === 401) {
                Alert.alert("Session Expired", "Please login again.");
                router.replace("/login");
            } else {
                Alert.alert('Error', 'Failed to load advert requests');
            }
        }
    };

    const loadData = async () => {
        setLoading(true);
        await fetchRequests();
        setLoading(false);
    };

    const onRefresh = async () => {
        setRefreshing(true);
        await fetchRequests();
        setRefreshing(false);
    };

    useEffect(() => {
        if (token) {
            loadData();
        }
    }, [token]);

    const handleApprove = async (productId: string) => {
        try {
            setApprovingId(productId);

            const currentToken = await api.getStoredToken();
            if (!currentToken) {
                Alert.alert("Session Expired", "Please login again.");
                router.replace("/login");
                return;
            }

            const response = await api.toggleFeatured(productId);

            if (response.success && response.data) {
                Alert.alert(
                    'Approved Successfully',
                    'Product has been approved and featured.',
                    [{ text: 'OK', onPress: () => { } }]
                );
                setProducts(prev => prev.filter(product => product._id !== productId));
            } else {
                if (response.message === "Please authenticate") {
                    Alert.alert("Session Expired", "Please login again.");
                    router.replace("/login");
                } else {
                    Alert.alert('Error', response.message || 'Failed to approve request');
                }
            }
        } catch (error: any) {
            console.error('Error approving request:', error);
            if (error.message?.includes("authenticate") || error.response?.status === 401) {
                Alert.alert("Session Expired", "Please login again.");
                router.replace("/login");
            } else {
                Alert.alert('Error', error.message || 'An unexpected error occurred');
            }
        } finally {
            setApprovingId(null);
        }
    };

    const handleReject = async (productId: string) => {
        Alert.alert(
            'Reject Advert Request',
            'Are you sure you want to reject this advert request? This action cannot be undone.',
            [
                {
                    text: 'Cancel',
                    style: 'cancel',
                },
                {
                    text: 'Reject',
                    style: 'destructive',
                    onPress: async () => {
                        try {
                            setRejectingId(productId);
                            const currentToken = await api.getStoredToken();
                            if (!currentToken) {
                                Alert.alert("Session Expired", "Please login again.");
                                router.replace("/login");
                                return;
                            }

                            const formData = new FormData();
                            formData.append('status', 'rejected');

                            const response = await api.updateProduct(productId, formData);

                            if (response.success) {
                                Alert.alert(
                                    'Rejected Successfully',
                                    'Request has been rejected.',
                                    [{ text: 'OK', onPress: () => { } }]
                                );
                                setProducts(prev => prev.filter(product => product._id !== productId));
                            } else {
                                if (response.message === "Please authenticate") {
                                    Alert.alert("Session Expired", "Please login again.");
                                    router.replace("/login");
                                } else {
                                    Alert.alert('Error', response.message || 'Failed to reject request');
                                }
                            }
                        } catch (error: any) {
                            console.error('Error rejecting request:', error);
                            if (error.message?.includes("authenticate") || error.response?.status === 401) {
                                Alert.alert("Session Expired", "Please login again.");
                                router.replace("/login");
                            } else {
                                Alert.alert('Error', 'Failed to reject request');
                            }
                        } finally {
                            setRejectingId(null);
                        }
                    }
                }
            ]
        );
    };

    const handleLogout = async () => {
        Alert.alert(
            'Logout',
            'Are you sure you want to logout?',
            [
                { text: 'Cancel', style: 'cancel' },
                {
                    text: 'Logout',
                    style: 'destructive',
                    onPress: async () => {
                        try {
                            await api.clearAuth();
                            router.replace("/login");
                        } catch (error) {
                            console.error("Logout error:", error);
                        }
                    }
                }
            ]
        );
    };

    const filteredProducts = products.filter(product =>
        product.name?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        product.categoryName?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        product.description?.toLowerCase().includes(searchQuery.toLowerCase())
    );

    const formatDate = (dateString: string) => {
        const date = new Date(dateString);
        return date.toLocaleDateString('en-US', {
            day: 'numeric',
            month: 'short',
            year: 'numeric'
        });
    };

    const formatPrice = (price: number) => {
        return new Intl.NumberFormat('en-US', {
            style: 'currency',
            currency: 'USD',
            minimumFractionDigits: 0,
            maximumFractionDigits: 0,
        }).format(price);
    };

    const renderItem = ({ item }: { item: Product }) => (
        <View style={styles.itemCard}>
            {/* Product Header */}
            <View style={styles.itemHeader}>
                <View style={styles.productTitleContainer}>
                    <MaterialCommunityIcons
                        name="tag-outline"
                        size={20}
                        color={COLORS.primary}
                        style={styles.titleIcon}
                    />
                    <Text style={styles.itemName} numberOfLines={1}>{item.name}</Text>
                </View>
                <View style={styles.categoryBadge}>
                    <Feather name="folder" size={12} color={COLORS.textLight} />
                    <Text style={styles.categoryText}>
                        {item.categoryName || 'Uncategorized'}
                    </Text>
                </View>
            </View>

            {/* Product Description */}
            <Text style={styles.itemDescription} numberOfLines={3}>
                {item.description || 'No description provided'}
            </Text>

            {/* Product Details */}
            <View style={styles.detailsContainer}>
                <View style={styles.detailRow}>
                    <View style={styles.detailItem}>
                        <Feather name="dollar-sign" size={14} color={COLORS.success} />
                        <Text style={styles.detailLabel}>Price</Text>
                        <Text style={styles.detailValue}>{formatPrice(item.price || 0)}</Text>
                    </View>

                    <View style={styles.detailItem}>
                        <Feather name="calendar" size={14} color={COLORS.primary} />
                        <Text style={styles.detailLabel}>Submitted</Text>
                        <Text style={styles.detailValue}>
                            {item.createdAt ? formatDate(item.createdAt) : 'N/A'}
                        </Text>
                    </View>
                </View>

                {
                    //@ts-ignore
                    item.merchant && (
                        <View style={styles.merchantInfo}>
                            <Feather name="user" size={14} color={COLORS.textMuted} />
                            <Text style={styles.merchantText}>

                                {
                                    //@ts-ignore
                                    item.merchant.name || 'Merchant'
                                }
                            </Text>
                        </View>
                    )}
            </View>

            {/* Action Buttons */}
            <View style={styles.actionButtonsContainer}>
                <TouchableOpacity
                    style={[styles.actionButton, styles.rejectButton]}
                    onPress={() => handleReject(item._id)}
                    disabled={rejectingId === item._id}
                >
                    {rejectingId === item._id ? (
                        <ActivityIndicator size="small" color="#fff" />
                    ) : (
                        <>
                            <Feather name="x" size={18} color="#fff" />
                            <Text style={styles.rejectButtonText}>Reject</Text>
                        </>
                    )}
                </TouchableOpacity>

                <TouchableOpacity
                    style={[styles.actionButton, styles.approveButton]}
                    onPress={() => handleApprove(item._id)}
                    disabled={approvingId === item._id}
                >
                    {approvingId === item._id ? (
                        <ActivityIndicator size="small" color="#fff" />
                    ) : (
                        <>
                            <Feather name="check" size={18} color="#fff" />
                            <Text style={styles.approveButtonText}>Approve</Text>
                        </>
                    )}
                </TouchableOpacity>
            </View>
        </View>
    );

    const renderEmptyState = () => (
        <View style={styles.emptyState}>
            <View style={styles.emptyIconContainer}>
                <MaterialCommunityIcons
                    name="clipboard-check-outline"
                    size={80}
                    color={COLORS.border}
                />
            </View>
            <Text style={styles.emptyTitle}>
                {searchQuery ? 'No matching requests' : 'No pending requests'}
            </Text>
            <Text style={styles.emptyDescription}>
                {searchQuery
                    ? 'Try adjusting your search terms'
                    : 'All advert requests have been reviewed'
                }
            </Text>
            {searchQuery ? (
                <TouchableOpacity
                    style={styles.emptyActionButton}
                    onPress={() => setSearchQuery('')}
                >
                    <Text style={styles.emptyActionText}>Clear Search</Text>
                </TouchableOpacity>
            ) : (
                <TouchableOpacity
                    style={[styles.emptyActionButton, styles.refreshButton]}
                    onPress={onRefresh}
                >
                    <Feather name="refresh-cw" size={16} color={COLORS.primary} />
                    <Text style={[styles.emptyActionText, { color: COLORS.primary }]}>
                        Refresh
                    </Text>
                </TouchableOpacity>
            )}
        </View>
    );

    if (!token && loading) {
        return (
            <View style={styles.loadingContainer}>
                <ActivityIndicator size="large" color={COLORS.primary} />
                <Text style={styles.loadingText}>Checking authentication...</Text>
            </View>
        );
    }

    if (loading && !refreshing) {
        return (
            <View style={styles.loadingContainer}>
                <ActivityIndicator size="large" color={COLORS.primary} />
                <Text style={styles.loadingText}>Loading requests...</Text>
            </View>
        );
    }

    return (
        <SafeAreaView style={styles.container}>
            {/* Header */}
            <View style={styles.header}>
                <View style={styles.headerContent}>
                    <View>
                        <View style={styles.titleRow}>
                            <Ionicons name="shield-checkmark" size={24} color={COLORS.primary} />
                            <Text style={styles.title}>Advert Review</Text>
                        </View>
                        <Text style={styles.subtitle}>
                            {products.length} pending request{products.length !== 1 ? 's' : ''} • Admin Panel
                        </Text>
                    </View>

                    <TouchableOpacity
                        style={styles.headerButton}
                        onPress={handleLogout}
                    >
                        <Feather name="log-out" size={20} color={COLORS.textLight} />
                    </TouchableOpacity>
                </View>
            </View>

            {/* Search Bar */}
            <View style={styles.searchContainer}>
                <Feather name="search" size={20} color={COLORS.textMuted} style={styles.searchIcon} />
                <TextInput
                    style={styles.searchInput}
                    placeholder="Search requests by name, category, or description..."
                    placeholderTextColor={COLORS.textMuted}
                    value={searchQuery}
                    onChangeText={setSearchQuery}
                    clearButtonMode="while-editing"
                />
                {searchQuery.length > 0 && (
                    <TouchableOpacity
                        style={styles.clearButton}
                        onPress={() => setSearchQuery('')}
                    >
                        <Feather name="x" size={18} color={COLORS.textMuted} />
                    </TouchableOpacity>
                )}
            </View>

            {/* Stats Bar */}
            <View style={styles.statsContainer}>
                <View style={styles.statItem}>
                    <Text style={styles.statNumber}>{products.length}</Text>
                    <Text style={styles.statLabel}>Total</Text>
                </View>
                <View style={styles.statDivider} />
                <View style={styles.statItem}>
                    <Text style={styles.statNumber}>
                        {filteredProducts.length}
                    </Text>
                    <Text style={styles.statLabel}>Showing</Text>
                </View>
                <View style={styles.statDivider} />
                <TouchableOpacity
                    style={styles.statItem}
                    onPress={onRefresh}
                >
                    <Feather name="refresh-cw" size={16} color={COLORS.primary} />
                    <Text style={[styles.statLabel, { color: COLORS.primary }]}>
                        Refresh
                    </Text>
                </TouchableOpacity>
            </View>

            {/* Product List */}
            <FlatList
                data={filteredProducts}
                keyExtractor={(item) => item._id}
                renderItem={renderItem}
                contentContainerStyle={styles.listContent}
                refreshing={refreshing}
                onRefresh={onRefresh}
                showsVerticalScrollIndicator={false}
                ListEmptyComponent={renderEmptyState}
                ListHeaderComponent={
                    filteredProducts.length > 0 ? (
                        <Text style={styles.listHeader}>
                            Review and approve advert requests
                        </Text>
                    ) : null
                }
            />
        </SafeAreaView>
    );
}

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: COLORS.background,
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: COLORS.background,
    },
    loadingText: {
        marginTop: 16,
        fontSize: 16,
        color: COLORS.textLight,
        fontFamily: 'System',
    },
    header: {
        backgroundColor: COLORS.card,
        borderBottomWidth: 1,
        borderBottomColor: COLORS.border,
        paddingTop: 12,
        paddingBottom: 16,
        paddingHorizontal: 20,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 8,
        elevation: 3,
    },
    headerContent: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
    },
    titleRow: {
        flexDirection: 'row',
        alignItems: 'center',
        gap: 12,
    },
    title: {
        fontSize: 28,
        fontWeight: '700',
        color: COLORS.text,
        fontFamily: 'System',
        letterSpacing: -0.5,
    },
    subtitle: {
        fontSize: 14,
        color: COLORS.textLight,
        marginTop: 4,
        fontFamily: 'System',
    },
    headerButton: {
        width: 44,
        height: 44,
        borderRadius: 22,
        backgroundColor: COLORS.background,
        justifyContent: 'center',
        alignItems: 'center',
        borderWidth: 1,
        borderColor: COLORS.border,
    },
    searchContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: COLORS.card,
        marginHorizontal: 20,
        marginTop: 16,
        marginBottom: 12,
        paddingHorizontal: 16,
        paddingVertical: 12,
        borderRadius: 16,
        borderWidth: 1,
        borderColor: COLORS.border,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 8,
        elevation: 2,
    },
    searchIcon: {
        marginRight: 12,
    },
    searchInput: {
        flex: 1,
        fontSize: 16,
        color: COLORS.text,
        fontFamily: 'System',
        padding: 0,
    },
    clearButton: {
        padding: 4,
    },
    statsContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: COLORS.card,
        marginHorizontal: 20,
        marginBottom: 16,
        paddingVertical: 12,
        paddingHorizontal: 16,
        borderRadius: 16,
        borderWidth: 1,
        borderColor: COLORS.border,
    },
    statItem: {
        flex: 1,
        alignItems: 'center',
    },
    statNumber: {
        fontSize: 20,
        fontWeight: '700',
        color: COLORS.text,
        fontFamily: 'System',
    },
    statLabel: {
        fontSize: 12,
        color: COLORS.textMuted,
        marginTop: 2,
        fontFamily: 'System',
    },
    statDivider: {
        width: 1,
        height: 24,
        backgroundColor: COLORS.border,
    },
    listContent: {
        paddingHorizontal: 20,
        paddingBottom: 24,
    },
    listHeader: {
        fontSize: 14,
        color: COLORS.textMuted,
        marginBottom: 16,
        fontFamily: 'System',
        textAlign: 'center',
    },
    itemCard: {
        backgroundColor: COLORS.card,
        borderRadius: 20,
        marginBottom: 16,
        padding: 20,
        borderWidth: 1,
        borderColor: COLORS.border,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.08,
        shadowRadius: 12,
        elevation: 4,
    },
    itemHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'flex-start',
        marginBottom: 12,
    },
    productTitleContainer: {
        flex: 1,
        flexDirection: 'row',
        alignItems: 'center',
    },
    titleIcon: {
        marginRight: 8,
    },
    itemName: {
        fontSize: 18,
        fontWeight: '600',
        color: COLORS.text,
        flex: 1,
        fontFamily: 'System',
    },
    categoryBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: `${COLORS.primary}10`,
        paddingHorizontal: 10,
        paddingVertical: 6,
        borderRadius: 12,
        gap: 6,
    },
    categoryText: {
        fontSize: 12,
        color: COLORS.textLight,
        fontWeight: '500',
        fontFamily: 'System',
    },
    itemDescription: {
        fontSize: 15,
        color: COLORS.textLight,
        lineHeight: 22,
        marginBottom: 16,
        fontFamily: 'System',
    },
    detailsContainer: {
        marginBottom: 20,
    },
    detailRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginBottom: 12,
    },
    detailItem: {
        flex: 1,
    },
    detailLabel: {
        fontSize: 12,
        color: COLORS.textMuted,
        marginTop: 4,
        fontFamily: 'System',
    },
    detailValue: {
        fontSize: 16,
        fontWeight: '600',
        color: COLORS.text,
        marginTop: 2,
        fontFamily: 'System',
    },
    merchantInfo: {
        flexDirection: 'row',
        alignItems: 'center',
        gap: 6,
    },
    merchantText: {
        fontSize: 13,
        color: COLORS.textMuted,
        fontFamily: 'System',
    },
    actionButtonsContainer: {
        flexDirection: 'row',
        gap: 12,
    },
    actionButton: {
        flex: 1,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        paddingVertical: 14,
        borderRadius: 14,
        gap: 8,
    },
    rejectButton: {
        backgroundColor: COLORS.error,
    },
    approveButton: {
        backgroundColor: COLORS.success,
    },
    rejectButtonText: {
        color: '#fff',
        fontSize: 16,
        fontWeight: '600',
        fontFamily: 'System',
    },
    approveButtonText: {
        color: '#fff',
        fontSize: 16,
        fontWeight: '600',
        fontFamily: 'System',
    },
    emptyState: {
        alignItems: 'center',
        paddingVertical: 60,
        paddingHorizontal: 40,
    },
    emptyIconContainer: {
        width: 120,
        height: 120,
        borderRadius: 60,
        backgroundColor: `${COLORS.border}20`,
        justifyContent: 'center',
        alignItems: 'center',
        marginBottom: 24,
    },
    emptyTitle: {
        fontSize: 22,
        fontWeight: '600',
        color: COLORS.text,
        marginBottom: 8,
        textAlign: 'center',
        fontFamily: 'System',
    },
    emptyDescription: {
        fontSize: 16,
        color: COLORS.textLight,
        textAlign: 'center',
        marginBottom: 24,
        lineHeight: 22,
        fontFamily: 'System',
    },
    emptyActionButton: {
        flexDirection: 'row',
        alignItems: 'center',
        gap: 8,
        paddingHorizontal: 24,
        paddingVertical: 12,
        borderRadius: 12,
        backgroundColor: `${COLORS.primary}10`,
    },
    refreshButton: {
        backgroundColor: `${COLORS.primary}10`,
    },
    emptyActionText: {
        fontSize: 16,
        fontWeight: '600',
        color: COLORS.textLight,
        fontFamily: 'System',
    },
});