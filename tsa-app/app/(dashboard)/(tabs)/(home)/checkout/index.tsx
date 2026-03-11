import React from 'react';
import { View, Text, StyleSheet, ScrollView, TouchableOpacity, Pressable } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { LinearGradient } from 'expo-linear-gradient';

const CheckoutScreen = () => {
    const [shippingAddress, setShippingAddress] = React.useState({
        name: 'John Doe',
        address: '123 Main Street',
        city: 'New York',
        state: 'NY',
        zipCode: '10001',
    });

    const orderItems = [
        { name: 'Product 1', quantity: 2, price: 45.00 },
        { name: 'Product 2', quantity: 1, price: 30.00 },
    ];

    const calculateSubtotal = () => {
        return orderItems.reduce((sum, item) => sum + (item.price * item.quantity), 0);
    };

    const subtotal = calculateSubtotal();
    const shipping = 5.00;
    const tax = 10.00;
    const total = subtotal + shipping + tax;

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
                            <Pressable style={styles.editButton}>
                                <Text style={styles.editText}>Change</Text>
                            </Pressable>
                        </View>
                        <View style={styles.card}>
                            <View style={styles.addressHeader}>
                                <Text style={styles.cardBoldText}>{shippingAddress.name}</Text>
                                <View style={styles.defaultBadge}>
                                    <Text style={styles.defaultBadgeText}>DEFAULT</Text>
                                </View>
                            </View>
                            <Text style={styles.cardText}>{shippingAddress.address}</Text>
                            <Text style={styles.cardText}>{shippingAddress.city}, {shippingAddress.state} {shippingAddress.zipCode}</Text>
                        </View>
                    </View>

                    {/* Payment Method Section */}
                    <View style={styles.section}>
                        <View style={styles.sectionHeader}>
                            <View style={styles.sectionTitleContainer}>
                                <Ionicons name="card-outline" size={20} color="#8B5A2B" />
                                <Text style={styles.sectionTitle}>Payment Method</Text>
                            </View>
                            <Pressable style={styles.editButton}>
                                <Text style={styles.editText}>Change</Text>
                            </Pressable>
                        </View>
                        <View style={styles.cardRow}>
                            <View style={styles.cardIconContainer}>
                                <Ionicons name="card" size={24} color="#FFF" />
                            </View>
                            <View style={styles.cardDetails}>
                                <Text style={styles.cardNumber}>**** **** **** 4242</Text>
                                <Text style={styles.cardExpiry}>Expires 12/25</Text>
                            </View>
                            <View style={styles.cardBrand}>
                                <Text style={styles.cardBrandText}>VISA</Text>
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
                            <Text style={styles.itemCount}>{orderItems.length} items</Text>
                        </View>

                        {orderItems.map((item, index) => (
                            <View key={index} style={styles.orderItem}>
                                <View style={styles.itemImagePlaceholder}>
                                    <Ionicons name="image-outline" size={24} color="#D9B68B" />
                                </View>
                                <View style={styles.itemDetails}>
                                    <Text style={styles.itemName}>{item.name}</Text>
                                    <Text style={styles.itemQuantity}>Qty: {item.quantity}</Text>
                                </View>
                                <Text style={styles.itemPrice}>${(item.price * item.quantity).toFixed(2)}</Text>
                            </View>
                        ))}
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
                                <Text style={styles.summaryValue}>${subtotal.toFixed(2)}</Text>
                            </View>
                            <View style={styles.summaryRow}>
                                <Text style={styles.summaryLabel}>Shipping</Text>
                                <Text style={styles.summaryValue}>${shipping.toFixed(2)}</Text>
                            </View>
                            <View style={styles.summaryRow}>
                                <Text style={styles.summaryLabel}>Tax</Text>
                                <Text style={styles.summaryValue}>${tax.toFixed(2)}</Text>
                            </View>

                            <View style={styles.divider} />

                            <View style={styles.totalRow}>
                                <Text style={styles.totalLabel}>Total</Text>
                                <View style={styles.totalAmountContainer}>
                                    <Text style={styles.currencySymbol}>USD</Text>
                                    <Text style={styles.totalValue}>${total.toFixed(2)}</Text>
                                </View>
                            </View>
                        </View>
                    </View>

                    {/* Delivery Estimate */}
                    <View style={[styles.section, styles.deliverySection]}>
                        <Ionicons name="time-outline" size={20} color="#8B5A2B" />
                        <Text style={styles.deliveryText}>
                            Estimated delivery: <Text style={styles.deliveryDate}>Mar 15 - Mar 18</Text>
                        </Text>
                    </View>
                </ScrollView>

                <View style={styles.footer}>
                    <View style={styles.footerTotal}>
                        <Text style={styles.footerTotalLabel}>Total Amount</Text>
                        <Text style={styles.footerTotalValue}>${total.toFixed(2)}</Text>
                    </View>

                    <TouchableOpacity
                        style={styles.checkoutButton}
                        activeOpacity={0.8}
                    >
                        <LinearGradient
                            colors={['#8B5A2B', '#6B4226']}
                            start={{ x: 0, y: 0 }}
                            end={{ x: 1, y: 0 }}
                            style={styles.gradientButton}
                        >
                            <Text style={styles.checkoutButtonText}>Place Order</Text>
                            <Ionicons name="arrow-forward" size={20} color="#FFF" />
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
    editButton: {
        paddingHorizontal: 12,
        paddingVertical: 4,
    },
    editText: {
        color: '#8B5A2B',
        fontWeight: '600',
        fontSize: 14,
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
    addressHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 8,
    },
    defaultBadge: {
        backgroundColor: '#F5E6D3',
        paddingHorizontal: 8,
        paddingVertical: 2,
        borderRadius: 12,
    },
    defaultBadgeText: {
        color: '#8B5A2B',
        fontSize: 10,
        fontWeight: '700',
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
    cardBrand: {
        backgroundColor: '#F5E6D3',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 8,
    },
    cardBrandText: {
        color: '#8B5A2B',
        fontWeight: '700',
        fontSize: 12,
    },
    cardBoldText: {
        fontWeight: '600',
        color: '#4A2C1A',
        fontSize: 16,
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
    deliverySection: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#FFF9F2',
        padding: 16,
        borderRadius: 12,
        gap: 8,
        borderWidth: 1,
        borderColor: '#E8D5C0',
        borderStyle: 'dashed',
    },
    deliveryText: {
        fontSize: 14,
        color: '#4A2C1A',
        flex: 1,
    },
    deliveryDate: {
        fontWeight: '700',
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