import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  Pressable,
  ActivityIndicator,
  Alert,
  Image,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { LinearGradient } from 'expo-linear-gradient';
import { useAuth } from '@/AuthContext/AuthContext';
import { cartService, CartSummaryResponse, CartItem, Product } from '@/components/services/cart';
import {
  createOrders,
  prepareEscrow,
  submitEscrow,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import LocationPicker from "@/components/common/LocationPicker";

type CheckoutStep = 'review' | 'signing' | 'success';
type SigningPhase =
  | 'idle'
  | 'creating_orders'
  | 'preparing_escrow'
  | 'approving_token'
  | 'broadcasting_approve'
  | 'creating_escrow'
  | 'broadcasting_escrow'
  | 'submitting'
  | 'done';

const SUPPORTED_TOKENS = ['USDC', 'USDT', 'MCGP'] as const;

const CheckoutScreen = () => {
  const { currentUser, token } = useAuth();
  const [cartData, setCartData] = useState<CartSummaryResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Escrow flow state
  const [step, setStep] = useState<CheckoutStep>('review');
  const [selectedToken, setSelectedToken] = useState<string>('USDC');
  const [signingPhase, setSigningPhase] = useState<SigningPhase>('idle');
  const [currentOrderIndex, setCurrentOrderIndex] = useState(0);
  const [orders, setOrders] = useState<Order[]>([]);
  const [completedOrders, setCompletedOrders] = useState<string[]>([]);
  const [signingError, setSigningError] = useState<string | null>(null);
  const [editingAddress, setEditingAddress] = useState(false);
  const [shippingLocation, setShippingLocation] = useState({
    country: "",
    state: "",
    city: "",
  });

  const loadCart = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
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
    } catch (err: any) {
      setError(err.message || 'Failed to load cart');
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    loadCart();
  }, [loadCart]);

  useEffect(() => {
    setShippingLocation({
      country: currentUser?.country || "",
      state: currentUser?.state || "",
      city: currentUser?.city || "",
    });
  }, [currentUser]);

  const getProductData = (item: CartItem): { name: string; imageUrl: string | null; productId: string } => {
    if (typeof item.product === 'object') {
      const product = item.product as Product;
      const primaryImage = product.images?.find((img) => img.isPrimary) || product.images?.[0];
      return {
        name: product.name,
        imageUrl: primaryImage?.url || null,
        productId: product._id,
      };
    }
    return { name: 'Product', imageUrl: null, productId: typeof item.product === 'string' ? item.product : '' };
  };

  const shippingName = currentUser?.name || currentUser?.username || '';
  const shippingAddress = {
    address: currentUser?.address || '',
    city: shippingLocation.city,
    state: shippingLocation.state,
    country: shippingLocation.country,
  };

  // --- Main escrow checkout flow ---
  const handleCreateOrders = async () => {
    // Validate shipping address
    if (!shippingLocation.country || !shippingLocation.state) {
      Alert.alert('Shipping Address Required', 'Please set your shipping country and state before checking out.');
      setEditingAddress(true);
      return;
    }

    if (!cartData || cartData.cart.items.length === 0) {
      Alert.alert('Empty Cart', 'Add items to your cart before checking out.');
      return;
    }

    setStep('signing');
    setSigningPhase('creating_orders');
    setSigningError(null);
    setCompletedOrders([]);

    try {
      // Step 1: Create orders
      const buyerCity = shippingLocation.city;
      const buyerState = shippingLocation.state;
      const buyerCountry = shippingLocation.country;

      const createResult = await createOrders(selectedToken, buyerCity, buyerState, buyerCountry);
      if (!createResult.success || !createResult.data?.orders) {
        throw new Error(createResult.message || 'Failed to create orders');
      }

      const newOrders = createResult.data.orders;
      setOrders(newOrders);
      setCurrentOrderIndex(0);

      // Step 2: Process each order
      for (let i = 0; i < newOrders.length; i++) {
        setCurrentOrderIndex(i);
        const order = newOrders[i];

        // Prepare escrow
        setSigningPhase('preparing_escrow');
        const escrowResult = await prepareEscrow(order.id);
        if (!escrowResult.success || !escrowResult.data) {
          throw new Error(escrowResult.message || 'Failed to prepare escrow');
        }

        const { approveTx, createOrderTx } = escrowResult.data;

        // Sign and broadcast approve tx
        setSigningPhase('approving_token');
        let approveTxHash: string;
        try {
          approveTxHash = await signAndBroadcast(approveTx);
        } catch (approveErr: any) {
          throw new Error(`Token approval failed: ${approveErr.message}`);
        }

        // Sign and broadcast createOrder tx
        // C3: If this fails, the approve succeeded but escrow didn't — tell user
        setSigningPhase('creating_escrow');
        let escrowTxHash: string;
        try {
          escrowTxHash = await signAndBroadcast(createOrderTx);
        } catch (escrowErr: any) {
          throw new Error(
            `Token was approved (tx: ${approveTxHash.slice(0, 10)}...) but escrow creation failed: ${escrowErr.message}. ` +
            `The allowance will be used on retry. Check your order list.`
          );
        }

        // Submit to backend
        setSigningPhase('submitting');
        const submitResult = await submitEscrow(order.id, approveTxHash, escrowTxHash);
        if (!submitResult.success) {
          throw new Error(submitResult.message || 'Failed to submit escrow');
        }

        setCompletedOrders((prev) => [...prev, order.id]);
      }

      setSigningPhase('done');
      setStep('success');
    } catch (err: any) {
      console.error('Checkout escrow error:', err);
      setSigningError(err.message || 'Something went wrong during checkout');
      setSigningPhase('idle');
    }
  };

  const getSigningStatusText = (): string => {
    const orderNum = orders.length > 1 ? ` (Order ${currentOrderIndex + 1} of ${orders.length})` : '';
    switch (signingPhase) {
      case 'creating_orders':
        return 'Creating orders...';
      case 'preparing_escrow':
        return `Preparing escrow${orderNum}...`;
      case 'approving_token':
        return `Approving ${selectedToken} transfer${orderNum}...`;
      case 'broadcasting_approve':
        return `Broadcasting approval${orderNum}...`;
      case 'creating_escrow':
        return `Creating escrow payment${orderNum}...`;
      case 'broadcasting_escrow':
        return `Broadcasting escrow${orderNum}...`;
      case 'submitting':
        return `Confirming on-chain${orderNum}...`;
      case 'done':
        return 'All orders escrowed!';
      default:
        return '';
    }
  };

  // --- Loading state ---
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

  // --- Error state ---
  if (error && step === 'review') {
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

  // --- Empty cart ---
  const items = cartData?.cart.items || [];
  const summary = cartData?.summary || {
    subtotal: 0,
    shipping: 0,
    platformFee: 0,
    discount: 0,
    total: 0,
    totalItems: 0,
    totalQuantity: 0,
  };
  const feeConfig = cartData?.feeConfig;
  const platformFeePercent = feeConfig?.platformFeePercent ?? 10;
  const isMCGP = selectedToken === 'MCGP';
  const effectiveFeePercent = isMCGP ? 0 : platformFeePercent;
  const effectivePlatformFee = isMCGP ? 0 : (summary.platformFee ?? summary.subtotal * platformFeePercent / 100);
  const estimatedTotal = summary.subtotal + summary.shipping + effectivePlatformFee - summary.discount;

  if (items.length === 0 && step === 'review') {
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

  // --- Success screen ---
  if (step === 'success') {
    return (
      <SafeAreaView style={styles.container}>
        <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
          <View style={styles.centered}>
            <View style={styles.successIcon}>
              <Ionicons name="checkmark-circle" size={72} color="#2E7D32" />
            </View>
            <Text style={styles.successTitle}>Orders Escrowed Successfully!</Text>
            <Text style={styles.successSubtitle}>
              {orders.length} order{orders.length !== 1 ? 's' : ''} created and funded
            </Text>
            {orders.map((order) => (
              <View key={order.id} style={styles.successOrderId}>
                <Text style={styles.successOrderIdText}>
                  Order #{order.id.slice(0, 8)}...
                </Text>
                <Text style={styles.successOrderAmount}>
                  {formatTokenAmount(order.totalAmount, order.token)}
                </Text>
              </View>
            ))}

            <TouchableOpacity
              style={styles.successButton}
              onPress={() => router.push('/(dashboard)/orderlist')}
            >
              <LinearGradient
                colors={['#8B5A2B', '#6B4226']}
                start={{ x: 0, y: 0 }}
                end={{ x: 1, y: 0 }}
                style={styles.gradientButton}
              >
                <Ionicons name="receipt-outline" size={20} color="#FFF" />
                <Text style={styles.checkoutButtonText}>View Orders</Text>
              </LinearGradient>
            </TouchableOpacity>

            <TouchableOpacity
              style={[styles.successButton, { marginTop: 12 }]}
              onPress={() => router.replace('/home')}
            >
              <View style={[styles.gradientButton, { backgroundColor: '#FFF', borderWidth: 1, borderColor: '#E8D5C0' }]}>
                <Text style={[styles.checkoutButtonText, { color: '#8B5A2B' }]}>Continue Shopping</Text>
              </View>
            </TouchableOpacity>
          </View>
        </LinearGradient>
      </SafeAreaView>
    );
  }

  // --- Signing screen ---
  if (step === 'signing') {
    return (
      <SafeAreaView style={styles.container}>
        <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
          <View style={styles.header}>
            <View style={{ width: 40 }} />
            <Text style={styles.headerTitle}>Processing</Text>
            <View style={{ width: 24 }} />
          </View>
          <View style={styles.centered}>
            {signingError ? (
              <>
                <Ionicons name="alert-circle-outline" size={48} color="#EF4444" />
                <Text style={styles.signingErrorTitle}>Transaction Failed</Text>
                <Text style={styles.signingErrorText}>{signingError}</Text>
                {completedOrders.length > 0 && (
                  <Text style={styles.signingPartialText}>
                    {completedOrders.length} of {orders.length} orders completed.
                    Check your order list for details.
                  </Text>
                )}
                {completedOrders.length > 0 ? (
                  <TouchableOpacity
                    style={[styles.retryButton, { backgroundColor: '#2E7D32' }]}
                    onPress={() => router.replace('/(dashboard)/orderlist')}
                  >
                    <Text style={styles.retryText}>View Orders</Text>
                  </TouchableOpacity>
                ) : (
                  <TouchableOpacity
                    style={styles.retryButton}
                    onPress={() => {
                      setStep('review');
                      setSigningError(null);
                      setSigningPhase('idle');
                      setOrders([]);
                      setCompletedOrders([]);
                    }}
                  >
                    <Text style={styles.retryText}>Back to Checkout</Text>
                  </TouchableOpacity>
                )}
              </>
            ) : (
              <>
                <ActivityIndicator size="large" color="#8B5A2B" />
                <Text style={styles.signingStatusText}>{getSigningStatusText()}</Text>
                {orders.length > 1 && (
                  <View style={styles.progressBar}>
                    <View
                      style={[
                        styles.progressFill,
                        { width: `${((completedOrders.length) / orders.length) * 100}%` },
                      ]}
                    />
                  </View>
                )}
                <Text style={styles.signingHint}>
                  Please wait. Do not close the app.
                </Text>
              </>
            )}
          </View>
        </LinearGradient>
      </SafeAreaView>
    );
  }

  // --- Review screen (main checkout) ---
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

        <ScrollView showsVerticalScrollIndicator={false} contentContainerStyle={styles.scrollContent}>
          {/* Shipping Address */}
          <View style={styles.section}>
            <View style={styles.sectionHeader}>
              <View style={styles.sectionTitleContainer}>
                <Ionicons name="location-outline" size={20} color="#8B5A2B" />
                <Text style={styles.sectionTitle}>Shipping Address</Text>
              </View>
              <TouchableOpacity onPress={() => setEditingAddress(!editingAddress)}>
                <Text style={{ color: '#8B5A2B', fontWeight: '600' }}>
                  {editingAddress ? 'Done' : 'Edit'}
                </Text>
              </TouchableOpacity>
            </View>
            <View style={styles.card}>
              {editingAddress ? (
                <LocationPicker
                  value={shippingLocation}
                  onChange={setShippingLocation}
                />
              ) : (
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
                  {!shippingAddress.country && (
                    <TouchableOpacity onPress={() => setEditingAddress(true)}>
                      <Text style={{ color: '#8B5A2B', fontWeight: '600', marginTop: 8 }}>
                        Add shipping address
                      </Text>
                    </TouchableOpacity>
                  )}
                </>
              )}
            </View>
          </View>

          {/* Payment Token Selection */}
          <View style={styles.section}>
            <View style={styles.sectionHeader}>
              <View style={styles.sectionTitleContainer}>
                <Ionicons name="wallet-outline" size={20} color="#8B5A2B" />
                <Text style={styles.sectionTitle}>Payment Token</Text>
              </View>
            </View>
            <View style={styles.tokenRow}>
              {SUPPORTED_TOKENS.map((tk) => (
                <TouchableOpacity
                  key={tk}
                  style={[
                    styles.tokenPill,
                    selectedToken === tk && styles.tokenPillActive,
                  ]}
                  onPress={() => setSelectedToken(tk)}
                >
                  <Text
                    style={[
                      styles.tokenPillText,
                      selectedToken === tk && styles.tokenPillTextActive,
                    ]}
                  >
                    {tk}
                  </Text>
                </TouchableOpacity>
              ))}
            </View>
            {isMCGP ? (
              <Text style={styles.tokenHint}>MCGP: No platform fee applied</Text>
            ) : feeConfig ? (
              <Text style={styles.tokenHint}>
                You'll receive {feeConfig.buyerCashbackPercent}% cashback on confirmation
              </Text>
            ) : null}
          </View>

          {/* Order Items */}
          <View style={styles.section}>
            <View style={styles.sectionHeader}>
              <View style={styles.sectionTitleContainer}>
                <Ionicons name="bag-outline" size={20} color="#8B5A2B" />
                <Text style={styles.sectionTitle}>Order Items</Text>
              </View>
              <Text style={styles.itemCount}>
                {summary.totalQuantity} item{summary.totalQuantity !== 1 ? 's' : ''}
              </Text>
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
                    <Text style={styles.itemName} numberOfLines={2}>
                      {name}
                    </Text>
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
              {!isMCGP && (
                <View style={styles.summaryRow}>
                  <Text style={styles.summaryLabel}>Platform Fee ({effectiveFeePercent}%)</Text>
                  <Text style={styles.summaryValue}>
                    ${effectivePlatformFee.toFixed(2)}
                  </Text>
                </View>
              )}
              {summary.discount > 0 && (
                <View style={styles.summaryRow}>
                  <Text style={styles.summaryLabel}>Discount</Text>
                  <Text style={[styles.summaryValue, { color: '#2E7D32' }]}>
                    -${summary.discount.toFixed(2)}
                  </Text>
                </View>
              )}

              <View style={styles.divider} />

              <View style={styles.totalRow}>
                <Text style={styles.totalLabel}>Estimated Total</Text>
                <View style={styles.totalAmountContainer}>
                  <Text style={styles.currencySymbol}>{selectedToken}</Text>
                  <Text style={styles.totalValue}>
                    ${estimatedTotal.toFixed(2)}
                  </Text>
                </View>
              </View>
              <Text style={styles.weiNote}>
                Exact amounts calculated on-chain in {selectedToken}
              </Text>
            </View>
          </View>
        </ScrollView>

        <View style={styles.footer}>
          <View style={styles.footerTotal}>
            <Text style={styles.footerTotalLabel}>Pay with {selectedToken}</Text>
            <Text style={styles.footerTotalValue}>
              ${(
                summary.subtotal +
                summary.shipping +
                (selectedToken !== 'MCGP' ? summary.subtotal * 0.1 : 0) -
                summary.discount
              ).toFixed(2)}
            </Text>
          </View>

          <TouchableOpacity
            style={styles.checkoutButton}
            activeOpacity={0.8}
            onPress={handleCreateOrders}
          >
            <LinearGradient
              colors={['#8B5A2B', '#6B4226']}
              start={{ x: 0, y: 0 }}
              end={{ x: 1, y: 0 }}
              style={styles.gradientButton}
            >
              <Ionicons name="shield-checkmark-outline" size={20} color="#FFF" />
              <Text style={styles.checkoutButtonText}>Create Order & Pay</Text>
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
    paddingHorizontal: 24,
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
  // Token selector
  tokenRow: {
    flexDirection: 'row',
    gap: 10,
  },
  tokenPill: {
    flex: 1,
    paddingVertical: 14,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: '#E8D5C0',
    backgroundColor: '#FFF',
    alignItems: 'center',
  },
  tokenPillActive: {
    borderColor: '#8B5A2B',
    backgroundColor: '#8B5A2B',
  },
  tokenPillText: {
    fontSize: 15,
    fontWeight: '600',
    color: '#8B5A2B',
  },
  tokenPillTextActive: {
    color: '#FFF',
  },
  tokenHint: {
    marginTop: 8,
    fontSize: 12,
    color: '#2E7D32',
    fontStyle: 'italic',
  },
  // Items
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
  // Summary
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
  weiNote: {
    fontSize: 11,
    color: '#A67C52',
    marginTop: 8,
    textAlign: 'right',
  },
  // Footer
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
    borderRadius: 16,
  },
  checkoutButtonText: {
    color: '#FFF',
    fontSize: 16,
    fontWeight: '700',
    letterSpacing: 0.5,
  },
  // Signing screen
  signingStatusText: {
    fontSize: 18,
    fontWeight: '600',
    color: '#4A2C1A',
    textAlign: 'center',
  },
  signingHint: {
    fontSize: 13,
    color: '#A67C52',
    marginTop: 8,
  },
  signingErrorTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#EF4444',
  },
  signingErrorText: {
    fontSize: 14,
    color: '#4A2C1A',
    textAlign: 'center',
    lineHeight: 20,
  },
  signingPartialText: {
    fontSize: 13,
    color: '#A67C52',
    textAlign: 'center',
    marginTop: 4,
  },
  progressBar: {
    width: '80%',
    height: 6,
    backgroundColor: '#E8D5C0',
    borderRadius: 3,
    marginTop: 16,
    overflow: 'hidden',
  },
  progressFill: {
    height: '100%',
    backgroundColor: '#8B5A2B',
    borderRadius: 3,
  },
  // Success screen
  successIcon: {
    marginBottom: 8,
  },
  successTitle: {
    fontSize: 22,
    fontWeight: '700',
    color: '#2E7D32',
    textAlign: 'center',
  },
  successSubtitle: {
    fontSize: 14,
    color: '#A67C52',
    marginBottom: 16,
  },
  successOrderId: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    backgroundColor: '#FFF',
    padding: 12,
    borderRadius: 10,
    borderWidth: 1,
    borderColor: '#E8D5C0',
    width: '100%',
    marginBottom: 8,
  },
  successOrderIdText: {
    fontSize: 14,
    fontWeight: '500',
    color: '#4A2C1A',
  },
  successOrderAmount: {
    fontSize: 14,
    fontWeight: '600',
    color: '#8B5A2B',
  },
  successButton: {
    width: '100%',
    borderRadius: 16,
    overflow: 'hidden',
    marginTop: 16,
  },
});

export default CheckoutScreen;
