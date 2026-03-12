import React, { useEffect, useState, useCallback } from 'react';
import { View, Text, StyleSheet, ScrollView, TouchableOpacity, Pressable, ActivityIndicator, Alert, Image } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { LinearGradient } from 'expo-linear-gradient';
import { useAuth } from '@/AuthContext/AuthContext';
import { cartService, CartSummaryResponse, CartItem, Product } from '@/components/services/cart';

const CheckoutScreen = () => {
    const { currentUser, token } = useAuth();
    const [cartData, setCartData] = useState<CartSummaryResponse | null>(null);
    const [loading, setLoading] = useState(true);
    const [checkingOut, setCheckingOut] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const loadCart = useCallback(async () => {
        setLoading(true);
        setError(null);
        if (token) {
            cartService.setToken(token);
        } else {
            await cartService.initializeToken();
        }
        const response = await cartService.getCartSummary();
        if (response.success && response.data) {
            setCartData(response.data);
        } else {
            setError(response.message || 'Failed to load cart');
        }
        setLoading(false);
    }, [token]);

    useEffect(() => {
        loadCart();
    }, [loadCart]);

    const handleCheckout = async () => {
        if (!cartData || cartData.cart.items.length === 0) {
            Alert.alert('Empty Cart', 'Add items to your cart before checking out.');
            return;
        }

        setCheckingOut(true);
        const response = await cartService.checkout();
        setCheckingOut(false);

        if (response.success && response.data) {
            Alert.alert(
                'Order Placed',
                `Your order #${response.data.orderId} has been placed successfully.`,
                [{ text: 'OK', onPress: () => router.replace('/(dashboard)/(tabs)/(home)') }]
            );
        } else {
            Alert.alert('Checkout Failed', response.message || 'Something went wrong. Please try again.');
        }
    };

    const getProductData = (item: CartItem): { name: string; imageUrl: string | null } => {
        if (typeof item.product === 'object') {
            const product = item.product as Product;
            const primaryImage = product.images?.find(img => img.isPrimary) || product.images?.[0];
            return {
                name: product.name,
                imageUrl: primaryImage?.url || null,
            };
        }
        return { name: 'Product', imageUrl: null };
    };

    const shippingName = currentUser?.name || currentUser?.username || '';
    const shippingAddress = cartData?.shippingAddress || {
        address: currentUser?.address || '',
        city: currentUser?.city || '',
        state: currentUser?.state || '',
        country: currentUser?.country || '',
    };

    if (loading) {
        return (
            <SafeAreaView style={styles.container}>
                <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
                    <View style={styles.header}>
                        <Pressable onPress={() => router.back()} style={styles.backButton}>
                            <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
                        </Pressable>
                        <Text style={styles.headerTitle}>Checkout</Text>
                        <View style={{ width: 24 }} />
                    </View>
                    <View style={styles.centered}>
                        <ActivityIndicator size="large" color="#8B5A2B" />
                        <Text style={styles.loadingText}>Loading cart...</Text>
                    </View>
                </LinearGradient>
            </SafeAreaView>
        );
    }

    if (error) {
        return (
            <SafeAreaView style={styles.container}>
                <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
                    <View style={styles.header}>
                        <Pressable onPress={() => router.back()} style={styles.backButton}>
                            <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
                        </Pressable>
                        <Text style={styles.headerTitle}>Checkout</Text>
                        <View style={{ width: 24 }} />
                    </View>
                    <View style={styles.centered}>
                        <Ionicons name="alert-circle-outline" size={48} color="#8B5A2B" />
                        <Text style={styles.errorText}>{error}</Text>
                        <TouchableOpacity onPress={loadCart} style={styles.retryButton}>
                            <Text style={styles.retryText}>Try Again</Text>
                        </TouchableOpacity>
                    </View>
                </LinearGradient>
            </SafeAreaView>
        );
    }

    const items = cartData?.cart.items || [];
    const summary = cartData?.summary || { subtotal: 0, shipping: 0, tax: 0, discount: 0, total: 0, totalItems: 0, totalQuantity: 0 };

    if (items.length === 0) {
        return (
            <SafeAreaView style={styles.container}>
                <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
                    <View style={styles.header}>
                        <Pressable onPress={() => router.back()} style={styles.backButton}>
                            <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
                        </Pressable>
                        <Text style={styles.headerTitle}>Checkout</Text>
                        <View style={{ width: 24 }} />
                    </View>
                    <View style={styles.centered}>
                        <Ionicons name="cart-outline" size={48} color="#D9B68B" />
                        <Text style={styles.emptyText}>Your cart is empty</Text>
                        <TouchableOpacity onPress={() => router.back()} style={styles.retryButton}>
                            <Text style={styles.retryText}>Continue Shopping</Text>
                        </TouchableOpacity>
                    </View>
                </LinearGradient>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={styles.container}>
            <LinearGradient
                colors={['#FDF8F3', '#FAF0E6']}
                style={styles.gradientBackground}
            >
                <View style={styles.header}>
                    <Pressable onPress={() => router.back()} style={styles.backButton}>
                        <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
                    </Pressable>
                    <Text style={styles.headerTitle}>Checkout</Text>
                    <View style={{ width: 24 }} />
                </View>

                <ScrollView
                    showsVerticalScrollIndicator={false}
                    contentContainerStyle={styles.scrollContent}
                >
                    {/* Shipping Address Section */}
                    <View style={styles.section}>
                        <View style={styles.sectionHeader}>
                            <View style={styles.sectionTitleContainer}>
                                <Ionicons name="location-outline" size={20} color="#8B5A2B" />
                                <Text style={styles.sectionTitle}>Shipping Address</Text>
                            </View>
                        </View>
                        <View style={styles.card}>
                            {shippingName ? (
                                <>
                                    <Text style={styles.cardBoldText}>{shippingName}</Text>
                                    {shippingAddress.address ? (
                                        <Text style={styles.cardText}>{shippingAddress.address}</Text>
                                    ) : null}
                                    <Text style={styles.cardText}>
                                        {[shippingAddress.city, shippingAddress.state, shippingAddress.country]
                                            .filter(Boolean)
                                            .join(', ')}
                                    </Text>
                                </>
                            ) : (
                                <Text style={styles.cardText}>No shipping address on file. Please update your profile.</Text>
                            )}
                        </View>
                    </View>

                    {/* Payment Method Section */}
                    <View style={styles.section}>
                        <View style={styles.sectionHeader}>
                            <View style={styles.sectionTitleContainer}>
                                <Ionicons name="wallet-outline" size={20} color="#8B5A2B" />
                                <Text style={styles.sectionTitle}>Payment</Text>
                            </View>
                        </View>
                        <View style={styles.cardRow}>
                            <View style={styles.cardIconContainer}>
                                <Ionicons name="wallet" size={24} color="#FFF" />
                            </View>
                            <View style={styles.cardDetails}>
                                <Text style={styles.cardNumber}>Wallet Payment</Text>
                                <Text style={styles.cardExpiry}>USDT / USDC / MCGP</Text>
                            </View>
                        </View>
                    </View>

                    {/* Order Items */}
                    <View style={styles.section}>
                        <View style={styles.sectionHeader}>
                            <View style={styles.sectionTitleContainer}>
                                <Ionicons name="bag-outline" size={20} color="#8B5A2B" />
                                <Text style={styles.sectionTitle}>Order Items</Text>
                            </View>
                            <Text style={styles.itemCount}>{summary.totalQuantity} item{summary.totalQuantity !== 1 ? 's' : ''}</Text>
                        </View>

                        {items.map((item, index) => {
                            const { name, imageUrl } = getProductData(item);
                            return (
                                <View key={item._id || index} style={styles.orderItem}>
                                    <View style={styles.itemImagePlaceholder}>
                                        {imageUrl ? (
                                            <Image source={{ uri: imageUrl }} style={styles.itemImage} />
                                        ) : (
                                            <Ionicons name="image-outline" size={24} color="#D9B68B" />
                                        )}
                                    </View>
                                    <View style={styles.itemDetails}>
                                        <Text style={styles.itemName} numberOfLines={2}>{name}</Text>
                                        <Text style={styles.itemQuantity}>Qty: {item.quantity}</Text>
                                    </View>
                                    <Text style={styles.itemPrice}>${(item.price * item.quantity).toFixed(2)}</Text>
                                </View>
                            );
                        })}
                    </View>

                    {/* Order Summary */}
                    <View style={[styles.section, styles.summarySection]}>
                        <View style={styles.sectionTitleContainer}>
                            <Ionicons name="receipt-outline" size={20} color="#8B5A2B" />
                            <Text style={styles.sectionTitle}>Order Summary</Text>
                        </View>

                        <View style={styles.summaryContainer}>
                            <View style={styles.summaryRow}>
                                <Text style={styles.summaryLabel}>Subtotal</Text>
                                <Text style={styles.summaryValue}>${summary.subtotal.toFixed(2)}</Text>
                            </View>
                            <View style={styles.summaryRow}>
                                <Text style={styles.summaryLabel}>Shipping</Text>
                                <Text style={styles.summaryValue}>${summary.shipping.toFixed(2)}</Text>
                            </View>
                            {summary.discount > 0 && (
                                <View style={styles.summaryRow}>
                                    <Text style={styles.summaryLabel}>Discount</Text>
                                    <Text style={[styles.summaryValue, { color: '#2E7D32' }]}>-${summary.discount.toFixed(2)}</Text>
                                </View>
                            )}

                            <View style={styles.divider} />

                            <View style={styles.totalRow}>
                                <Text style={styles.totalLabel}>Total</Text>
                                <View style={styles.totalAmountContainer}>
                                    <Text style={styles.currencySymbol}>USD</Text>
                                    <Text style={styles.totalValue}>${summary.total.toFixed(2)}</Text>
                                </View>
                            </View>
                        </View>
                    </View>
                </ScrollView>

                <View style={styles.footer}>
                    <View style={styles.footerTotal}>
                        <Text style={styles.footerTotalLabel}>Total Amount</Text>
                        <Text style={styles.footerTotalValue}>${summary.total.toFixed(2)}</Text>
                    </View>

                    <TouchableOpacity
                        style={styles.checkoutButton}
                        activeOpacity={0.8}
                        onPress={handleCheckout}
                        disabled={checkingOut}
                    >
                        <LinearGradient
                            colors={checkingOut ? ['#B89B7A', '#A68B6A'] : ['#8B5A2B', '#6B4226']}
                            start={{ x: 0, y: 0 }}
                            end={{ x: 1, y: 0 }}
                            style={styles.gradientButton}
                        >
                            {checkingOut ? (
                                <ActivityIndicator size="small" color="#FFF" />
                            ) : (
                                <>
                                    <Text style={styles.checkoutButtonText}>Place Order</Text>
                                    <Ionicons name="arrow-forward" size={20} color="#FFF" />
                                </>
                            )}
                        </LinearGradient>
                    </TouchableOpacity>
                </View>
            </LinearGradient>
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
    },
    gradientBackground: {
        flex: 1,
    },
    header: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingHorizontal: 20,
        paddingVertical: 16,
        backgroundColor: 'transparent',
    },
    backButton: {
        width: 40,
        height: 40,
        borderRadius: 20,
        backgroundColor: '#FFF',
        justifyContent: 'center',
        alignItems: 'center',
        shadowColor: '#8B5A2B',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
        elevation: 2,
    },
    headerTitle: {
        fontSize: 20,
        fontWeight: '700',
        color: '#4A2C1A',
    },
    scrollContent: {
        padding: 20,
        paddingTop: 8,
    },
    centered: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        gap: 12,
    },
    loadingText: {
        color: '#8B5A2B',
        fontSize: 16,
    },
    errorText: {
        color: '#4A2C1A',
        fontSize: 16,
        textAlign: 'center',
        paddingHorizontal: 32,
    },
    emptyText: {
        color: '#A67C52',
        fontSize: 16,
    },
    retryButton: {
        marginTop: 8,
        paddingHorizontal: 24,
        paddingVertical: 10,
        backgroundColor: '#8B5A2B',
        borderRadius: 12,
    },
    retryText: {
        color: '#FFF',
        fontWeight: '600',
    },
    section: {
        marginBottom: 24,
    },
    sectionHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 12,
    },
    sectionTitleContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        gap: 8,
    },
    sectionTitle: {
        fontSize: 16,
        fontWeight: '600',
        color: '#4A2C1A',
    },
    card: {
        backgroundColor: '#FFF',
        padding: 16,
        borderRadius: 16,
        borderWidth: 1,
        borderColor: '#E8D5C0',
        shadowColor: '#8B5A2B',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    cardRow: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#FFF',
        padding: 16,
        borderRadius: 16,
        borderWidth: 1,
        borderColor: '#E8D5C0',
        shadowColor: '#8B5A2B',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    cardIconContainer: {
        width: 48,
        height: 48,
        borderRadius: 12,
        backgroundColor: '#8B5A2B',
        justifyContent: 'center',
        alignItems: 'center',
    },
    cardDetails: {
        flex: 1,
        marginLeft: 12,
    },
    cardNumber: {
        fontSize: 16,
        fontWeight: '600',
        color: '#4A2C1A',
        marginBottom: 2,
    },
    cardExpiry: {
        fontSize: 12,
        color: '#A67C52',
    },
    cardBoldText: {
        fontWeight: '600',
        color: '#4A2C1A',
        fontSize: 16,
        marginBottom: 4,
    },
    cardText: {
        color: '#A67C52',
        lineHeight: 20,
    },
    itemCount: {
        color: '#8B5A2B',
        fontWeight: '500',
        fontSize: 14,
    },
    orderItem: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#FFF',
        padding: 12,
        borderRadius: 12,
        marginBottom: 8,
        borderWidth: 1,
        borderColor: '#F0E0D0',
    },
    itemImagePlaceholder: {
        width: 50,
        height: 50,
        borderRadius: 8,
        backgroundColor: '#F5E6D3',
        justifyContent: 'center',
        alignItems: 'center',
        overflow: 'hidden',
    },
    itemImage: {
        width: 50,
        height: 50,
        borderRadius: 8,
    },
    itemDetails: {
        flex: 1,
        marginLeft: 12,
    },
    itemName: {
        fontSize: 14,
        fontWeight: '600',
        color: '#4A2C1A',
        marginBottom: 2,
    },
    itemQuantity: {
        fontSize: 12,
        color: '#A67C52',
    },
    itemPrice: {
        fontSize: 16,
        fontWeight: '700',
        color: '#8B5A2B',
    },
    summarySection: {
        backgroundColor: '#FFF',
        borderRadius: 16,
        padding: 16,
        borderWidth: 1,
        borderColor: '#E8D5C0',
    },
    summaryContainer: {
        marginTop: 12,
    },
    summaryRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginBottom: 12,
    },
    summaryLabel: {
        color: '#A67C52',
        fontSize: 14,
    },
    summaryValue: {
        fontWeight: '600',
        color: '#4A2C1A',
        fontSize: 14,
    },
    divider: {
        height: 1,
        backgroundColor: '#E8D5C0',
        marginVertical: 12,
    },
    totalRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
    },
    totalLabel: {
        fontSize: 18,
        fontWeight: '700',
        color: '#4A2C1A',
    },
    totalAmountContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        gap: 8,
    },
    currencySymbol: {
        fontSize: 14,
        color: '#A67C52',
        fontWeight: '500',
    },
    totalValue: {
        fontSize: 24,
        fontWeight: '800',
        color: '#8B5A2B',
    },
    footer: {
        padding: 20,
        backgroundColor: '#FFF',
        borderTopWidth: 1,
        borderTopColor: '#E8D5C0',
        shadowColor: '#8B5A2B',
        shadowOffset: { width: 0, height: -4 },
        shadowOpacity: 0.05,
        shadowRadius: 8,
        elevation: 10,
    },
    footerTotal: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 16,
    },
    footerTotalLabel: {
        fontSize: 14,
        color: '#A67C52',
    },
    footerTotalValue: {
        fontSize: 24,
        fontWeight: '800',
        color: '#8B5A2B',
    },
    checkoutButton: {
        borderRadius: 16,
        overflow: 'hidden',
    },
    gradientButton: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        paddingVertical: 18,
        paddingHorizontal: 24,
        gap: 8,
    },
    checkoutButtonText: {
        color: '#FFF',
        fontSize: 16,
        fontWeight: '700',
        letterSpacing: 0.5,
    },
});

export default CheckoutScreen;