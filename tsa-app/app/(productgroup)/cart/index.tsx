// app/(productgroup)/cart/index.tsx
//
// Shopping cart — items grouped by seller, quantity steppers, order summary
// (subtotal / shipping / gas fee / discount / total) and checkout. Restyled onto
// the TSA Signature design system (theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: every cart fetch, quantity update, remove/clear, validation,
// money formatting (formatPrice util preserved — it owns the USD→local rate
// conversion) and checkout navigation is preserved verbatim. This is a
// visual/structural restyle only.
import React, { useEffect, useState, useCallback } from "react";
import {
    View,
    Image,
    FlatList,
    Alert,
    RefreshControl,
} from "react-native";
import { useRouter } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";
import AsyncStorage from "@react-native-async-storage/async-storage";
import api, { CartItem, CartSummary, ItemsBySeller } from "@/components/services/cart";
import { useCurrency } from "@/contexts/CurrencyContext";
import { formatPrice as formatPriceUtil } from "@/utils/formatPrice";
import { useTheme } from "@/theme";
import {
    AccentCard,
    Avatar,
    Badge,
    Button,
    Card,
    Divider,
    EmptyState,
    Icon,
    PressableScale,
    Spinner,
    SummaryCard,
    SummaryRow,
    Text,
} from "@/components/ui";

const Cart = () => {
    const router = useRouter();
    const { colors, spacing, radius } = useTheme();
    const [cartItems, setCartItems] = useState<CartItem[]>([]);
    const [summary, setSummary] = useState<CartSummary | null>(null);
    const [itemsBySeller, setItemsBySeller] = useState<ItemsBySeller[]>([]);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [updatingItem, setUpdatingItem] = useState<string | null>(null);
    const { currency, rate } = useCurrency();

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

    const formatPrice = (price: number) =>
        formatPriceUtil(price, { currency, rate });

    const renderSellerSection = ({ item }: { item: ItemsBySeller }) => (
        <Card padding="lg" style={{ marginHorizontal: spacing.lg, marginTop: spacing.lg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Avatar
                    name={item.seller.companyName || item.seller.name || item.seller.email || 'S'}
                    size={40}
                />
                <View style={{ flex: 1 }}>
                    <Text variant="bodyMed" weight="600" numberOfLines={1}>
                        {item.seller.companyName || item.seller.name || 'Seller'}
                    </Text>
                    <Text variant="caption" color="textSecondary">
                        Subtotal: {formatPrice(item.subtotal)}
                    </Text>
                </View>
            </View>

            <Divider spacing="md" />

            {item.items.map((cartItem, index) => (
                <React.Fragment key={cartItem._id || `cart-item-${index}`}>
                    {renderCartItem(cartItem, index, item.items.length)}
                </React.Fragment>
            ))}
        </Card>
    );

    const renderCartItem = (item: CartItem, index: number, count: number) => {
        const rawProduct = item.product;
        const product = (rawProduct && typeof rawProduct === 'object') ? rawProduct : null;
        const productId = product?._id || (product as { id?: string })?.id || (typeof rawProduct === 'string' ? rawProduct : null);
        const isUpdating = updatingItem === item._id;
        const decrementDisabled = isUpdating || item.quantity <= 1;
        const incrementDisabled = isUpdating || (product?.stock != null && item.quantity >= product.stock);

        return (
            <View key={item._id}>
                <View style={{ flexDirection: 'row', alignItems: 'flex-start', paddingVertical: spacing.md }}>
                    <PressableScale
                        onPress={() => productId && router.push(`/product/${productId}`)}
                        disabled={!productId}
                        style={{ flex: 1, flexDirection: 'row', gap: spacing.md }}
                    >
                        {/* Product Image */}
                        <View
                            style={{
                                width: 80,
                                height: 80,
                                borderRadius: radius.md,
                                overflow: 'hidden',
                                backgroundColor: colors.surfaceAlt,
                                justifyContent: 'center',
                                alignItems: 'center',
                            }}
                        >
                            {product?.images?.find((img: any) => img.url)?.url ? (
                                <Image
                                    source={{ uri: product.images.find((img: any) => img.url)!.url }}
                                    style={{ width: '100%', height: '100%' }}
                                    resizeMode="cover"
                                />
                            ) : (
                                <Icon name="image" size={24} color={colors.textMuted} />
                            )}
                        </View>

                        {/* Product Details */}
                        <View style={{ flex: 1 }}>
                            <Text variant="bodySm" weight="500" numberOfLines={2}>
                                {product?.name || 'Product'}
                            </Text>

                            {/* Selected Attributes */}
                            {item.selectedAttributes && item.selectedAttributes.length > 0 && (
                                <View
                                    style={{
                                        flexDirection: 'row',
                                        flexWrap: 'wrap',
                                        gap: spacing.xs,
                                        marginTop: spacing.xs,
                                    }}
                                >
                                    {item.selectedAttributes.map((attr, idx) => (
                                        <Badge key={idx} label={`${attr.name}: ${attr.value}`} size="sm" />
                                    ))}
                                </View>
                            )}

                            {/* Price and Quantity */}
                            <View
                                style={{
                                    flexDirection: 'row',
                                    justifyContent: 'space-between',
                                    alignItems: 'center',
                                    marginTop: spacing.sm,
                                }}
                            >
                                <Text variant="bodyMed" weight="700" color="accent" numeric>
                                    {formatPrice(item.price)}
                                </Text>

                                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                                    <PressableScale
                                        onPress={() => handleUpdateQuantity(item._id, item.quantity - 1, product?.stock ?? 999)}
                                        disabled={decrementDisabled}
                                        accessibilityRole="button"
                                        accessibilityLabel="Decrease quantity"
                                        style={{
                                            width: 28,
                                            height: 28,
                                            borderRadius: radius.pill,
                                            borderWidth: 1,
                                            borderColor: decrementDisabled ? colors.border : colors.accent,
                                            justifyContent: 'center',
                                            alignItems: 'center',
                                            opacity: decrementDisabled ? 0.5 : 1,
                                        }}
                                    >
                                        <Text
                                            variant="bodyMed"
                                            weight="600"
                                            style={{ color: decrementDisabled ? colors.textMuted : colors.accent, lineHeight: 18 }}
                                        >
                                            −
                                        </Text>
                                    </PressableScale>

                                    <View style={{ minWidth: 24, alignItems: 'center' }}>
                                        {isUpdating ? (
                                            <Spinner size="small" />
                                        ) : (
                                            <Text variant="bodySm" weight="500" numeric>
                                                {item.quantity}
                                            </Text>
                                        )}
                                    </View>

                                    <PressableScale
                                        onPress={() => handleUpdateQuantity(item._id, item.quantity + 1, product?.stock ?? 999)}
                                        disabled={incrementDisabled}
                                        accessibilityRole="button"
                                        accessibilityLabel="Increase quantity"
                                        style={{
                                            width: 28,
                                            height: 28,
                                            borderRadius: radius.pill,
                                            borderWidth: 1,
                                            borderColor: incrementDisabled ? colors.border : colors.accent,
                                            justifyContent: 'center',
                                            alignItems: 'center',
                                            opacity: incrementDisabled ? 0.5 : 1,
                                        }}
                                    >
                                        <Icon
                                            name="plus"
                                            size={16}
                                            color={incrementDisabled ? colors.textMuted : colors.accent}
                                        />
                                    </PressableScale>
                                </View>
                            </View>

                            {/* Item Total */}
                            <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.xs }} numeric>
                                Total: {formatPrice(item.price * item.quantity)}
                            </Text>
                        </View>
                    </PressableScale>

                    {/* Remove Button */}
                    <PressableScale
                        onPress={() => handleRemoveItem(item._id)}
                        disabled={isUpdating}
                        accessibilityRole="button"
                        accessibilityLabel="Remove item"
                        hitSlop={8}
                        style={{ padding: spacing.sm, justifyContent: 'center' }}
                    >
                        <Icon name="trash" size={20} color={isUpdating ? colors.textMuted : colors.danger} />
                    </PressableScale>
                </View>
                {index < count - 1 && <Divider />}
            </View>
        );
    };

    const renderEmptyCart = () => (
        <EmptyState
            icon="cart"
            title="Your Cart is Empty"
            subtitle="Looks like you haven't added any items to your cart yet."
            actionLabel="Start Shopping"
            onAction={() => router.push('/')}
        />
    );

    const renderHeader = () => (
        <View
            style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
                paddingHorizontal: spacing.lg,
                paddingTop: spacing.sm,
            }}
        >
            <Text variant="title1" weight="700">
                Shopping Cart
            </Text>
            {cartItems.length > 0 && (
                <PressableScale onPress={handleClearCart} hitSlop={8}>
                    <Text variant="bodySm" weight="600" color="danger">
                        Clear All
                    </Text>
                </PressableScale>
            )}
        </View>
    );

    const renderFooter = () => {
        if (cartItems.length === 0) return null;

        return (
            <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.lg }}>
                {/* Summary Card */}
                <AccentCard padding="lg" style={{ marginBottom: spacing.lg }}>
                    <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
                        Order Summary
                    </Text>

                    <SummaryCard dividerBeforeLast>
                        <SummaryRow label="Subtotal" value={formatPrice(summary?.subtotal || 0)} />

                        <SummaryRow
                            label="Shipping"
                            value={summary?.shipping ? formatPrice(summary.shipping) : 'To be calculated'}
                        />

                        {(summary?.gasFee ?? 0) > 0 && (
                            <SummaryRow label="Gas Fee" value={formatPrice(summary!.gasFee)} />
                        )}

                        {summary?.discount ? (
                            <SummaryRow
                                label="Discount"
                                value={`-${formatPrice(summary.discount)}`}
                                valueColor="success"
                            />
                        ) : null}

                        <SummaryRow label="Total" value={formatPrice(summary?.total || 0)} total valueColor="accent" />
                    </SummaryCard>
                </AccentCard>

                {/* Checkout Button */}
                <Button
                    title={`Proceed to Checkout (${cartItems.length} ${cartItems.length === 1 ? 'item' : 'items'})`}
                    onPress={handleCheckout}
                    loading={loading}
                    disabled={loading}
                    size="lg"
                />
            </View>
        );
    };

    if (loading && !refreshing) {
        return (
            <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
                <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
                    <Spinner size="large" label="Loading your cart..." />
                </View>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            <FlatList
                data={itemsBySeller}
                renderItem={renderSellerSection}
                keyExtractor={(item, index) => item.seller._id || `seller-${index}`}
                ListHeaderComponent={renderHeader}
                ListEmptyComponent={renderEmptyCart}
                ListFooterComponent={renderFooter}
                contentContainerStyle={{ flexGrow: 1, paddingTop: spacing.md, paddingBottom: spacing['4xl'] }}
                showsVerticalScrollIndicator={false}
                refreshControl={
                    <RefreshControl
                        refreshing={refreshing}
                        onRefresh={onRefresh}
                        colors={[colors.accent]}
                        tintColor={colors.accent}
                    />
                }
            />
        </SafeAreaView>
    );
};

export default Cart;
