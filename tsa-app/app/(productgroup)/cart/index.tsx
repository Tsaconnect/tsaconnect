// app/cart/index.tsx
import React, { useEffect, useState, useCallback } from "react";
import {
    View,
    Text,
    StyleSheet,
    Image,
    FlatList,
    TouchableOpacity,
    Alert,
    ActivityIndicator,
    RefreshControl,
    StatusBar,
} from "react-native";
import { useRouter } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import { SafeAreaView } from "react-native-safe-area-context";
import AsyncStorage from "@react-native-async-storage/async-storage";
import api, { CartItem, CartSummary, ItemsBySeller } from "@/components/services/cart";

const Cart = () => {
    const router = useRouter();
    const [cartItems, setCartItems] = useState<CartItem[]>([]);
    const [summary, setSummary] = useState<CartSummary | null>(null);
    const [itemsBySeller, setItemsBySeller] = useState<ItemsBySeller[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [updatingItem, setUpdatingItem] = useState<string | null>(null);

    useEffect(() => {
        checkAuthAndLoadCart();
    }, []);

    const checkAuthAndLoadCart = async () => {
        try {
            const token = await AsyncStorage.getItem('authToken');
            if (token) {
                api.setToken(token);
                await loadCart();
            } else {
                setLoading(false);
                Alert.alert(
                    'Login Required',
                    'Please login to view your cart',
                    [
                        {
                            text: 'Login',
                            onPress: () => router.push('/login')
                        },
                        {
                            text: 'Cancel',
                            style: 'cancel'
                        }
                    ]
                );
            }
        } catch (error) {
            console.error('Auth check error:', error);
            setLoading(false);
        }
    };

    const loadCart = async () => {
        try {
            setLoading(true);
            const response = await api.getCartSummary();

            if (response.success && response.data) {
                setCartItems(response.data.cart.items || []);
                setSummary(response.data.summary);
                setItemsBySeller(response.data.itemsBySeller || []);
            } else {
                if (response.message?.includes('not found') || response.message?.includes('No cart')) {
                    // Cart doesn't exist yet - this is fine, just empty cart
                    setCartItems([]);
                    setSummary(null);
                    setItemsBySeller([]);
                } else {
                    Alert.alert('Error', response.message || 'Failed to load cart');
                }
            }
        } catch (error) {
            console.error('Load cart error:', error);
            Alert.alert('Error', 'Failed to load cart. Please try again.');
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    };

    const onRefresh = useCallback(() => {
        setRefreshing(true);
        loadCart();
    }, []);

    const handleUpdateQuantity = async (itemId: string, newQuantity: number, currentStock: number) => {
        if (newQuantity < 1) {
            // Remove item if quantity goes below 1
            handleRemoveItem(itemId);
            return;
        }

        if (newQuantity > currentStock) {
            Alert.alert('Stock Limit', `Only ${currentStock} items available in stock`);
            return;
        }

        try {
            setUpdatingItem(itemId);
            const response = await api.updateCartItem(itemId, { quantity: newQuantity });

            if (response.success && response.data) {
                // Update local state
                setCartItems(response.data.items || []);
                if (response.data.summary) {
                    setSummary(response.data.summary);
                }

                // Also refresh the full summary to get updated seller grouping
                await loadCart();
            } else {
                Alert.alert('Error', response.message || 'Failed to update quantity');
            }
        } catch (error) {
            console.error('Update quantity error:', error);
            Alert.alert('Error', 'Failed to update quantity. Please try again.');
        } finally {
            setUpdatingItem(null);
        }
    };

    const handleRemoveItem = async (itemId: string) => {
        Alert.alert(
            'Remove Item',
            'Are you sure you want to remove this item from your cart?',
            [
                { text: 'Cancel', style: 'cancel' },
                {
                    text: 'Remove',
                    style: 'destructive',
                    onPress: async () => {
                        try {
                            setUpdatingItem(itemId);
                            const response = await api.removeFromCart(itemId);

                            if (response.success && response.data) {
                                setCartItems(response.data.items || []);
                                if (response.data.summary) {
                                    setSummary(response.data.summary);
                                }
                                await loadCart(); // Refresh full data
                            } else {
                                Alert.alert('Error', response.message || 'Failed to remove item');
                            }
                        } catch (error) {
                            console.error('Remove item error:', error);
                            Alert.alert('Error', 'Failed to remove item. Please try again.');
                        } finally {
                            setUpdatingItem(null);
                        }
                    }
                }
            ]
        );
    };

    const handleClearCart = () => {
        if (cartItems.length === 0) return;

        Alert.alert(
            'Clear Cart',
            'Are you sure you want to remove all items from your cart?',
            [
                { text: 'Cancel', style: 'cancel' },
                {
                    text: 'Clear All',
                    style: 'destructive',
                    onPress: async () => {
                        try {
                            setLoading(true);
                            const response = await api.clearCart();

                            if (response.success) {
                                setCartItems([]);
                                setSummary(null);
                                setItemsBySeller([]);
                            } else {
                                Alert.alert('Error', response.message || 'Failed to clear cart');
                            }
                        } catch (error) {
                            console.error('Clear cart error:', error);
                            Alert.alert('Error', 'Failed to clear cart. Please try again.');
                        } finally {
                            setLoading(false);
                        }
                    }
                }
            ]
        );
    };

    const handleCheckout = async () => {
        if (cartItems.length === 0) {
            Alert.alert('Empty Cart', 'Your cart is empty. Add some items before checkout.');
            return;
        }

        try {
            setLoading(true);
            // Validate cart before checkout
            const validationResponse = await api.validateCart();

            if (validationResponse.success && validationResponse.data) {
                if (validationResponse.data.valid) {
                    router.push(`/checkout?totalAmount=${summary?.total || 0}&itemCount=${cartItems.length}`);
                } else {
                    // Show validation issues
                    const issues = validationResponse.data.issues;
                    if (issues.length > 0) {
                        Alert.alert(
                            'Cart Issues',
                            issues[0].issue || 'Some items in your cart have issues. Please review.',
                            [{ text: 'OK' }]
                        );
                    }
                }
            } else {
                Alert.alert('Error', validationResponse.message || 'Failed to validate cart');
            }
        } catch (error) {
            console.error('Checkout error:', error);
            Alert.alert('Error', 'Failed to proceed to checkout. Please try again.');
        } finally {
            setLoading(false);
        }
    };

    const formatPrice = (price: number) => {
        return `$${price.toLocaleString()}`;
    };

    const renderSellerSection = ({ item }: { item: ItemsBySeller }) => (
        <View style={styles.sellerSection}>
            <View style={styles.sellerHeader}>
                <View style={styles.sellerAvatar}>
                    <Text style={styles.sellerAvatarText}>
                        {(item.seller.companyName || item.seller.name || item.seller.email || 'S').charAt(0).toUpperCase()}
                    </Text>
                </View>
                <View style={styles.sellerInfo}>
                    <Text style={styles.sellerName}>{item.seller.companyName || item.seller.name || 'Seller'}</Text>
                    <Text style={styles.sellerSubtotal}>Subtotal: {formatPrice(item.subtotal)}</Text>
                </View>
            </View>

            {item.items.map((cartItem, index) => (
                <React.Fragment key={cartItem._id || `cart-item-${index}`}>
                    {renderCartItem(cartItem)}
                </React.Fragment>
            ))}
        </View>
    );

    const renderCartItem = (item: CartItem) => {
        const rawProduct = item.product;
        const product = (rawProduct && typeof rawProduct === 'object') ? rawProduct : null;
        const productId = product?._id || product?.id || (typeof rawProduct === 'string' ? rawProduct : null);
        const isUpdating = updatingItem === item._id;

        return (
            <View key={item._id} style={styles.itemCard}>
                <TouchableOpacity
                    style={styles.itemContent}
                    onPress={() => productId && router.push(`/product/${productId}`)}
                    disabled={!productId}
                >
                    {/* Product Image */}
                    <View style={styles.imageContainer}>
                        {product?.images && product.images.length > 0 ? (
                            <Image
                                source={{ uri: product.images[0].url }}
                                style={styles.itemImage}
                                resizeMode="cover"
                            />
                        ) : (
                            <View style={styles.imagePlaceholder}>
                                <Ionicons name="image-outline" size={24} color="#CCC" />
                            </View>
                        )}
                    </View>

                    {/* Product Details */}
                    <View style={styles.itemDetails}>
                        <Text style={styles.itemName} numberOfLines={2}>
                            {product?.name || 'Product'}
                        </Text>

                        {/* Selected Attributes */}
                        {item.selectedAttributes && item.selectedAttributes.length > 0 && (
                            <View style={styles.attributesContainer}>
                                {item.selectedAttributes.map((attr, index) => (
                                    <View key={index} style={styles.attributeBadge}>
                                        <Text style={styles.attributeText}>
                                            {attr.name}: {attr.value}
                                        </Text>
                                    </View>
                                ))}
                            </View>
                        )}

                        {/* Price and Quantity */}
                        <View style={styles.priceQuantityRow}>
                            <Text style={styles.itemPrice}>{formatPrice(item.price)}</Text>

                            <View style={styles.quantityControls}>
                                <TouchableOpacity
                                    style={[styles.quantityButton, isUpdating && styles.disabledButton]}
                                    onPress={() => handleUpdateQuantity(item._id, item.quantity - 1, product?.stock ?? 999)}
                                    disabled={isUpdating || item.quantity <= 1}
                                >
                                    <Ionicons
                                        name="remove"
                                        size={16}
                                        color={item.quantity <= 1 ? '#CCC' : '#D4AF37'}
                                    />
                                </TouchableOpacity>

                                <Text style={styles.quantityText}>
                                    {isUpdating ? (
                                        <ActivityIndicator size="small" color="#D4AF37" />
                                    ) : (
                                        item.quantity
                                    )}
                                </Text>

                                <TouchableOpacity
                                    style={[styles.quantityButton, isUpdating && styles.disabledButton]}
                                    onPress={() => handleUpdateQuantity(item._id, item.quantity + 1, product?.stock ?? 999)}
                                    disabled={isUpdating || (product?.stock != null && item.quantity >= product.stock)}
                                >
                                    <Ionicons
                                        name="add"
                                        size={16}
                                        color={(product?.stock != null && item.quantity >= product.stock) ? '#CCC' : '#D4AF37'}
                                    />
                                </TouchableOpacity>
                            </View>
                        </View>

                        {/* Item Total */}
                        <Text style={styles.itemTotal}>
                            Total: {formatPrice(item.price * item.quantity)}
                        </Text>
                    </View>
                </TouchableOpacity>

                {/* Remove Button */}
                <TouchableOpacity
                    style={styles.removeButton}
                    onPress={() => handleRemoveItem(item._id)}
                    disabled={isUpdating}
                >
                    <Ionicons
                        name="trash-outline"
                        size={20}
                        color={isUpdating ? '#CCC' : '#FF6B6B'}
                    />
                </TouchableOpacity>
            </View>
        );
    };

    const renderEmptyCart = () => (
        <View style={styles.emptyContainer}>
            <Ionicons name="cart-outline" size={80} color="#D4AF37" />
            <Text style={styles.emptyTitle}>Your Cart is Empty</Text>
            <Text style={styles.emptyText}>
                Looks like you haven't added any items to your cart yet.
            </Text>
            <TouchableOpacity
                style={styles.shopButton}
                onPress={() => router.push('/')}
            >
                <Text style={styles.shopButtonText}>Start Shopping</Text>
            </TouchableOpacity>
        </View>
    );

    const renderHeader = () => (
        <View style={styles.header}>
            <Text style={styles.headerTitle}>Shopping Cart</Text>
            {cartItems.length > 0 && (
                <TouchableOpacity onPress={handleClearCart}>
                    <Text style={styles.clearText}>Clear All</Text>
                </TouchableOpacity>
            )}
        </View>
    );

    const renderFooter = () => {
        if (cartItems.length === 0) return null;

        return (
            <View style={styles.footer}>
                {/* Summary Card */}
                <View style={styles.summaryCard}>
                    <Text style={styles.summaryTitle}>Order Summary</Text>

                    <View style={styles.summaryRow}>
                        <Text style={styles.summaryLabel}>Subtotal</Text>
                        <Text style={styles.summaryValue}>{formatPrice(summary?.subtotal || 0)}</Text>
                    </View>

                    <View style={styles.summaryRow}>
                        <Text style={styles.summaryLabel}>Shipping</Text>
                        <Text style={styles.summaryValue}>
                            {summary?.shipping ? formatPrice(summary.shipping) : 'To be calculated'}
                        </Text>
                    </View>

                    <View style={styles.summaryRow}>
                        <Text style={styles.summaryLabel}>Platform Fee (10%)</Text>
                        <Text style={styles.summaryValue}>
                            {(summary?.platformFee ?? summary?.tax) ? formatPrice(summary?.platformFee ?? summary?.tax ?? 0) : 'To be calculated'}
                        </Text>
                    </View>

                    {summary?.discount ? (
                        <View style={styles.summaryRow}>
                            <Text style={styles.summaryLabel}>Discount</Text>
                            <Text style={styles.discountValue}>-{formatPrice(summary.discount)}</Text>
                        </View>
                    ) : null}

                    <View style={[styles.summaryRow, styles.totalRow]}>
                        <Text style={styles.totalLabel}>Total</Text>
                        <Text style={styles.totalValue}>{formatPrice(summary?.total || 0)}</Text>
                    </View>
                </View>

                {/* Checkout Button */}
                <TouchableOpacity
                    style={styles.checkoutButton}
                    onPress={handleCheckout}
                    disabled={loading}
                >
                    {loading ? (
                        <ActivityIndicator color="#FFFFFF" />
                    ) : (
                        <>
                            <Text style={styles.checkoutButtonText}>Proceed to Checkout</Text>
                            <Text style={styles.checkoutItemCount}>
                                ({cartItems.length} {cartItems.length === 1 ? 'item' : 'items'})
                            </Text>
                        </>
                    )}
                </TouchableOpacity>
            </View>
        );
    };

    if (loading && !refreshing) {
        return (
            <SafeAreaView style={styles.safeArea}>
                <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />
                <View style={styles.loadingContainer}>
                    <ActivityIndicator size="large" color="#D4AF37" />
                    <Text style={styles.loadingText}>Loading your cart...</Text>
                </View>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={styles.safeArea}>
            <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

            <FlatList
                data={itemsBySeller}
                renderItem={renderSellerSection}
                keyExtractor={(item, index) => item.seller._id || `seller-${index}`}
                ListHeaderComponent={renderHeader}
                ListEmptyComponent={renderEmptyCart}
                ListFooterComponent={renderFooter}
                contentContainerStyle={styles.listContent}
                showsVerticalScrollIndicator={false}
                refreshControl={
                    <RefreshControl
                        refreshing={refreshing}
                        onRefresh={onRefresh}
                        colors={["#D4AF37"]}
                        tintColor="#D4AF37"
                    />
                }
            />
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    safeArea: {
        flex: 1,
        backgroundColor: '#F8F9FA',
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F8F9FA',
    },
    loadingText: {
        marginTop: 12,
        fontSize: 14,
        color: '#666',
    },
    listContent: {
        flexGrow: 1,
        paddingBottom: 20,
    },
    header: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 20,
        paddingVertical: 16,
        backgroundColor: '#FFFFFF',
        borderBottomWidth: 1,
        borderBottomColor: 'rgba(0,0,0,0.05)',
    },
    headerTitle: {
        fontSize: 24,
        fontWeight: '700',
        color: '#1A1A1A',
    },
    clearText: {
        fontSize: 14,
        color: '#FF6B6B',
        fontWeight: '600',
    },
    sellerSection: {
        marginBottom: 16,
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        marginHorizontal: 16,
        marginTop: 16,
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 8,
        elevation: 2,
    },
    sellerHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        padding: 16,
        backgroundColor: '#F8F9FA',
        borderBottomWidth: 1,
        borderBottomColor: 'rgba(0,0,0,0.05)',
    },
    sellerAvatar: {
        width: 40,
        height: 40,
        borderRadius: 20,
        backgroundColor: '#D4AF37',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    sellerAvatarText: {
        color: '#FFFFFF',
        fontSize: 16,
        fontWeight: '600',
    },
    sellerInfo: {
        flex: 1,
    },
    sellerName: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
        marginBottom: 2,
    },
    sellerSubtotal: {
        fontSize: 13,
        color: '#666',
    },
    itemCard: {
        flexDirection: 'row',
        padding: 16,
        backgroundColor: '#FFFFFF',
        borderBottomWidth: 1,
        borderBottomColor: 'rgba(0,0,0,0.05)',
    },
    itemContent: {
        flex: 1,
        flexDirection: 'row',
    },
    imageContainer: {
        width: 80,
        height: 80,
        borderRadius: 8,
        overflow: 'hidden',
        backgroundColor: '#F0F0F0',
        marginRight: 12,
    },
    itemImage: {
        width: '100%',
        height: '100%',
    },
    imagePlaceholder: {
        width: '100%',
        height: '100%',
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F0F0F0',
    },
    itemDetails: {
        flex: 1,
    },
    itemName: {
        fontSize: 14,
        fontWeight: '500',
        color: '#1A1A1A',
        marginBottom: 4,
    },
    attributesContainer: {
        flexDirection: 'row',
        flexWrap: 'wrap',
        marginBottom: 8,
    },
    attributeBadge: {
        backgroundColor: '#F0F0F0',
        paddingHorizontal: 8,
        paddingVertical: 2,
        borderRadius: 4,
        marginRight: 6,
        marginBottom: 4,
    },
    attributeText: {
        fontSize: 10,
        color: '#666',
    },
    priceQuantityRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 4,
    },
    itemPrice: {
        fontSize: 16,
        fontWeight: '700',
        color: '#D4AF37',
    },
    quantityControls: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    quantityButton: {
        width: 28,
        height: 28,
        borderRadius: 14,
        borderWidth: 1,
        borderColor: '#D4AF37',
        justifyContent: 'center',
        alignItems: 'center',
    },
    disabledButton: {
        opacity: 0.5,
        borderColor: '#CCC',
    },
    quantityText: {
        fontSize: 14,
        fontWeight: '500',
        color: '#1A1A1A',
        marginHorizontal: 8,
        minWidth: 20,
        textAlign: 'center',
    },
    itemTotal: {
        fontSize: 12,
        color: '#666',
    },
    removeButton: {
        padding: 8,
        justifyContent: 'center',
    },
    footer: {
        paddingHorizontal: 16,
        paddingTop: 16,
        paddingBottom: 30,
    },
    summaryCard: {
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        padding: 16,
        marginBottom: 16,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 8,
        elevation: 2,
    },
    summaryTitle: {
        fontSize: 18,
        fontWeight: '700',
        color: '#1A1A1A',
        marginBottom: 16,
    },
    summaryRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginBottom: 12,
    },
    summaryLabel: {
        fontSize: 14,
        color: '#666',
    },
    summaryValue: {
        fontSize: 14,
        fontWeight: '500',
        color: '#1A1A1A',
    },
    discountValue: {
        fontSize: 14,
        fontWeight: '500',
        color: '#4CAF50',
    },
    totalRow: {
        marginTop: 12,
        paddingTop: 12,
        borderTopWidth: 1,
        borderTopColor: 'rgba(0,0,0,0.05)',
    },
    totalLabel: {
        fontSize: 16,
        fontWeight: '700',
        color: '#1A1A1A',
    },
    totalValue: {
        fontSize: 18,
        fontWeight: '700',
        color: '#D4AF37',
    },
    checkoutButton: {
        backgroundColor: '#D4AF37',
        borderRadius: 12,
        padding: 16,
        flexDirection: 'row',
        justifyContent: 'center',
        alignItems: 'center',
        shadowColor: '#D4AF37',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 4,
    },
    checkoutButtonText: {
        color: '#FFFFFF',
        fontSize: 16,
        fontWeight: '700',
        marginRight: 8,
    },
    checkoutItemCount: {
        color: '#FFFFFF',
        fontSize: 14,
        opacity: 0.9,
    },
    emptyContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        paddingHorizontal: 40,
        paddingTop: 100,
    },
    emptyTitle: {
        fontSize: 20,
        fontWeight: '700',
        color: '#1A1A1A',
        marginTop: 20,
        marginBottom: 8,
    },
    emptyText: {
        fontSize: 14,
        color: '#666',
        textAlign: 'center',
        marginBottom: 24,
    },
    shopButton: {
        backgroundColor: '#D4AF37',
        paddingHorizontal: 32,
        paddingVertical: 14,
        borderRadius: 25,
        shadowColor: '#D4AF37',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 4,
    },
    shopButtonText: {
        color: '#FFFFFF',
        fontSize: 16,
        fontWeight: '600',
    },
});

export default Cart;