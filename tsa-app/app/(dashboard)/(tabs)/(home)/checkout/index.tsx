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
  Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '@/AuthContext/AuthContext';
import {
  cartService,
  CartSummaryResponse,
  CartItem,
  Product,
} from '@/components/services/cart';
import {
  createOrders,
  prepareApprove,
  prepareEscrow,
  submitEscrow,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import LocationPicker from '@/components/common/LocationPicker';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useKycVerification } from '../../../../../hooks/useKycVerification';

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

/** Parse blockchain/signing errors into user-friendly messages. */
function friendlyTxError(err: any): string {
  const msg = err?.message || err?.toString() || '';
  if (msg.includes('INSUFFICIENT_FUNDS') || msg.includes('insufficient funds'))
    return 'Not enough S (Sonic) in your wallet to cover gas fees. Please top up your wallet with S and try again.';
  if (msg.includes('user rejected') || msg.includes('User denied') || msg.includes('ACTION_REJECTED'))
    return 'Transaction was cancelled.';
  if (msg.includes('nonce'))
    return 'Transaction conflict — please wait a moment and try again.';
  if (msg.includes('UNPREDICTABLE_GAS_LIMIT') || msg.includes('execution reverted'))
    return 'Transaction would fail on-chain. Please check your token balance and try again.';
  return msg;
}

const TOKEN_META: Record<
  string,
  { icon: string; color: string; label: string }
> = {
  USDC: { icon: 'logo-usd', color: '#2775CA', label: 'USD Coin' },
  USDT: { icon: 'logo-usd', color: '#26A17B', label: 'Tether' },
  MCGP: { icon: 'diamond-outline', color: '#D4AF37', label: 'MCGP (0% fee)' },
};

const CheckoutScreen = () => {
  const { currentUser, token } = useAuth();
  const { requireKycVerified } = useKycVerification();
  const [cartData, setCartData] = useState<CartSummaryResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [step, setStep] = useState<CheckoutStep>('review');
  const [selectedToken, setSelectedToken] = useState<string>('USDC');
  const [signingPhase, setSigningPhase] = useState<SigningPhase>('idle');
  const [currentOrderIndex, setCurrentOrderIndex] = useState(0);
  const [orders, setOrders] = useState<Order[]>([]);
  const [completedOrders, setCompletedOrders] = useState<string[]>([]);
  const [signingError, setSigningError] = useState<string | null>(null);
  const [hasWallet, setHasWallet] = useState<boolean | null>(null);
  const [editingAddress, setEditingAddress] = useState(false);
  const [shippingLocation, setShippingLocation] = useState({
    country: '',
    state: '',
    city: '',
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
    // Check if wallet exists — use same check as wallet home screen
    (async () => {
      const addr = await AsyncStorage.getItem('walletAddress');
      setHasWallet(!!addr);
    })();
  }, [loadCart]);

  useEffect(() => {
    setShippingLocation({
      country: currentUser?.country || '',
      state: currentUser?.state || '',
      city: currentUser?.city || '',
    });
  }, [currentUser]);

  const getProductData = (item: CartItem) => {
    if (typeof item.product === 'object') {
      const product = item.product as Product;
      const img = product.images?.find((i: any) => i.url)?.url || null;
      return { name: product.name, imageUrl: img, productId: product._id };
    }
    return {
      name: 'Product',
      imageUrl: null,
      productId: typeof item.product === 'string' ? item.product : '',
    };
  };

  const shippingName = currentUser?.name || currentUser?.username || '';
  const shippingAddress = {
    address: currentUser?.address || '',
    city: shippingLocation.city,
    state: shippingLocation.state,
    country: shippingLocation.country,
  };

  // --- Escrow checkout flow ---
  const handleCreateOrders = async () => {
    if (!requireKycVerified()) return;
    // Re-check wallet in case user came back without completing setup
    const addr = await AsyncStorage.getItem('walletAddress');
    if (!addr) {
      Alert.alert(
        'Wallet Required',
        'Please set up your wallet first.',
        [{ text: 'Set Up Wallet', onPress: () => router.push('/wallet/home') }],
      );
      return;
    }

    if (!shippingLocation.country || !shippingLocation.state) {
      Alert.alert(
        'Shipping Required',
        'Please set your shipping address before checking out.',
      );
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
      const createResult = await createOrders(
        selectedToken,
        shippingLocation.city,
        shippingLocation.state,
        shippingLocation.country,
      );
      if (!createResult.success || !createResult.data?.orders) {
        throw new Error(createResult.message || 'Failed to create orders');
      }

      const newOrders = createResult.data.orders;
      setOrders(newOrders);
      setCurrentOrderIndex(0);

      for (let i = 0; i < newOrders.length; i++) {
        setCurrentOrderIndex(i);
        const order = newOrders[i];

        // Step A: Prepare & broadcast approve tx
        setSigningPhase('approving_token');
        const approveResult = await prepareApprove(order.id);
        if (!approveResult.success || !approveResult.data) {
          throw new Error(approveResult.message || 'Failed to prepare approval');
        }

        let approveTxHash: string;
        try {
          approveTxHash = await signAndBroadcast(approveResult.data.approveTx);
        } catch (e: any) {
          throw new Error(friendlyTxError(e));
        }

        // Step B: Prepare escrow tx (after approve is on-chain, so gas estimation works)
        setSigningPhase('creating_escrow');
        const escrowResult = await prepareEscrow(order.id);
        if (!escrowResult.success || !escrowResult.data) {
          throw new Error(escrowResult.message || 'Failed to prepare escrow');
        }

        let escrowTxHash: string;
        try {
          escrowTxHash = await signAndBroadcast(escrowResult.data.createOrderTx);
        } catch (e: any) {
          throw new Error(friendlyTxError(e));
        }

        setSigningPhase('submitting');
        const submitResult = await submitEscrow(
          order.id,
          approveTxHash,
          escrowTxHash,
        );
        if (!submitResult.success) {
          throw new Error(submitResult.message || 'Failed to submit escrow');
        }

        setCompletedOrders((prev) => [...prev, order.id]);
      }

      setSigningPhase('done');
      setStep('success');
    } catch (err: any) {
      console.error('Checkout escrow error:', err);
      setSigningError(err.message || 'Something went wrong');
      setSigningPhase('idle');
    }
  };

  const getSigningStatusText = (): string => {
    const n =
      orders.length > 1 ? ` (${currentOrderIndex + 1}/${orders.length})` : '';
    switch (signingPhase) {
      case 'creating_orders':
        return 'Creating orders...';
      case 'preparing_escrow':
        return `Preparing escrow${n}...`;
      case 'approving_token':
        return `Approving ${selectedToken}${n}...`;
      case 'creating_escrow':
        return `Creating escrow${n}...`;
      case 'submitting':
        return `Confirming on-chain${n}...`;
      case 'done':
        return 'All orders escrowed!';
      default:
        return '';
    }
  };

  // --- Derived data ---
  const items = cartData?.cart.items || [];
  const summary = cartData?.summary || {
    subtotal: 0,
    shipping: 0,
    gasFee: 0,
    platformFee: 0,
    discount: 0,
    total: 0,
    totalItems: 0,
    totalQuantity: 0,
  };
  const feeConfig = cartData?.feeConfig;
  const isMCGP = selectedToken === 'MCGP';
  const gasFee = isMCGP ? 0 : (summary.gasFee ?? feeConfig?.gasFeeUSD ?? 0.1);
  const estimatedTotal =
    summary.subtotal + summary.shipping + gasFee - summary.discount;

  // ===== HEADER =====
  const Header = ({
    title = 'Checkout',
    showBack = true,
  }: {
    title?: string;
    showBack?: boolean;
  }) => (
    <View style={s.header}>
      {showBack ? (
        <Pressable onPress={() => router.back()} style={s.backBtn}>
          <Ionicons name="chevron-back" size={22} color="#1A1A1A" />
        </Pressable>
      ) : (
        <View style={{ width: 40 }} />
      )}
      <Text style={s.headerTitle}>{title}</Text>
      <View style={{ width: 40 }} />
    </View>
  );

  // ===== LOADING =====
  if (loading) {
    return (
      <SafeAreaView style={s.safe}>
        <Header />
        <View style={s.center}>
          <ActivityIndicator size="large" color="#D4AF37" />
          <Text style={s.mutedText}>Loading your order...</Text>
        </View>
      </SafeAreaView>
    );
  }

  // ===== ERROR =====
  if (error && step === 'review') {
    return (
      <SafeAreaView style={s.safe}>
        <Header />
        <View style={s.center}>
          <View style={s.errorCircle}>
            <Ionicons name="alert-circle" size={40} color="#DC2626" />
          </View>
          <Text style={s.errorTitle}>Something went wrong</Text>
          <Text style={s.mutedText}>{error}</Text>
          <TouchableOpacity onPress={loadCart} style={s.primaryBtn}>
            <Text style={s.primaryBtnText}>Try Again</Text>
          </TouchableOpacity>
        </View>
      </SafeAreaView>
    );
  }

  // ===== EMPTY CART =====
  if (items.length === 0 && step === 'review') {
    return (
      <SafeAreaView style={s.safe}>
        <Header />
        <View style={s.center}>
          <Ionicons name="cart-outline" size={56} color="#CCC" />
          <Text style={s.errorTitle}>Your cart is empty</Text>
          <TouchableOpacity onPress={() => router.replace('/digital')} style={s.primaryBtn}>
            <Text style={s.primaryBtnText}>Start Shopping</Text>
          </TouchableOpacity>
        </View>
      </SafeAreaView>
    );
  }

  // ===== NO WALLET — redirect to wallet setup =====
  if (hasWallet === false && step === 'review') {
    Alert.alert(
      'Wallet Required',
      'You need a wallet to complete your purchase. Set up your wallet to continue.',
      [
        { text: 'Cancel', style: 'cancel', onPress: () => router.back() },
        { text: 'Set Up Wallet', onPress: () => router.push('/wallet/home') },
      ],
    );
    // Reset so the alert doesn't re-trigger
    setHasWallet(null);
  }

  // ===== SUCCESS =====
  if (step === 'success') {
    return (
      <SafeAreaView style={s.safe}>
        <View style={s.center}>
          <View style={s.successCircle}>
            <Ionicons name="checkmark" size={48} color="#FFF" />
          </View>
          <Text style={s.successTitle}>Payment Successful!</Text>
          <Text style={s.mutedText}>
            {orders.length} order{orders.length !== 1 ? 's' : ''} created &
            funded via escrow
          </Text>

          <View style={s.successOrders}>
            {orders.map((order) => (
              <View key={order.id} style={s.successOrderRow}>
                <View>
                  <Text style={s.successOrderId}>#{order.id.slice(0, 8)}</Text>
                  <Text style={s.successOrderToken}>{order.token}</Text>
                </View>
                <Text style={s.successOrderAmount}>
                  {formatTokenAmount(order.totalAmount, order.token)}
                </Text>
              </View>
            ))}
          </View>

          <TouchableOpacity
            style={s.primaryBtn}
            onPress={() => router.push('/(dashboard)/orderlist')}
          >
            <Ionicons name="receipt-outline" size={18} color="#FFF" />
            <Text style={s.primaryBtnText}>View Orders</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={s.outlineBtn}
            onPress={() => router.replace('/home')}
          >
            <Text style={s.outlineBtnText}>Continue Shopping</Text>
          </TouchableOpacity>
        </View>
      </SafeAreaView>
    );
  }

  // ===== SIGNING (PROCESSING) =====
  if (step === 'signing') {
    const progress =
      orders.length > 0 ? completedOrders.length / orders.length : 0;
    return (
      <SafeAreaView style={s.safe}>
        <Header title="Processing" showBack={false} />
        <View style={s.center}>
          {signingError ? (
            <>
              <View style={s.errorCircle}>
                <Ionicons name="close" size={36} color="#FFF" />
              </View>
              <Text style={s.errorTitle}>Transaction Failed</Text>
              <Text
                style={[s.mutedText, { textAlign: 'center', lineHeight: 20 }]}
              >
                {signingError}
              </Text>
              {completedOrders.length > 0 && (
                <Text style={s.partialText}>
                  {completedOrders.length}/{orders.length} orders completed
                </Text>
              )}
              <TouchableOpacity
                style={s.primaryBtn}
                onPress={() => {
                  if (completedOrders.length > 0) {
                    router.replace('/(dashboard)/orderlist');
                  } else {
                    setStep('review');
                    setSigningError(null);
                    setSigningPhase('idle');
                    setOrders([]);
                    setCompletedOrders([]);
                  }
                }}
              >
                <Text style={s.primaryBtnText}>
                  {completedOrders.length > 0
                    ? 'View Orders'
                    : 'Back to Checkout'}
                </Text>
              </TouchableOpacity>
            </>
          ) : (
            <>
              <View style={s.processingRing}>
                <ActivityIndicator size="large" color="#D4AF37" />
              </View>
              <Text style={s.processingTitle}>{getSigningStatusText()}</Text>
              {orders.length > 1 && (
                <View style={s.progressBar}>
                  <View
                    style={[s.progressFill, { width: `${progress * 100}%` }]}
                  />
                </View>
              )}
              <Text style={s.mutedText}>
                Please wait. Do not close the app.
              </Text>
            </>
          )}
        </View>
      </SafeAreaView>
    );
  }

  // ===== REVIEW (MAIN CHECKOUT) =====
  return (
    <SafeAreaView style={s.safe}>
      <Header />

      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={s.scroll}
      >
        {/* Step Indicators */}
        <View style={s.steps}>
          {['Review', 'Pay', 'Done'].map((label, i) => (
            <View key={label} style={s.stepItem}>
              <View style={[s.stepDot, i === 0 && s.stepDotActive]}>
                <Text style={[s.stepDotText, i === 0 && s.stepDotTextActive]}>
                  {i + 1}
                </Text>
              </View>
              <Text style={[s.stepLabel, i === 0 && s.stepLabelActive]}>
                {label}
              </Text>
            </View>
          ))}
        </View>

        {/* Shipping Address */}
        <View style={s.card}>
          <View style={s.cardHeader}>
            <View style={s.cardIconWrap}>
              <Ionicons name="location" size={16} color="#D4AF37" />
            </View>
            <Text style={s.cardTitle}>Shipping Address</Text>
            <TouchableOpacity
              onPress={() => setEditingAddress(!editingAddress)}
            >
              <Text style={s.editLink}>{editingAddress ? 'Done' : 'Edit'}</Text>
            </TouchableOpacity>
          </View>
          {editingAddress ? (
            <LocationPicker
              value={shippingLocation}
              onChange={setShippingLocation}
            />
          ) : shippingAddress.country ? (
            <View style={s.addressContent}>
              <Text style={s.addressName}>{shippingName}</Text>
              {shippingAddress.address ? (
                <Text style={s.addressLine}>{shippingAddress.address}</Text>
              ) : null}
              <Text style={s.addressLine}>
                {[
                  shippingAddress.city,
                  shippingAddress.state,
                  shippingAddress.country,
                ]
                  .filter(Boolean)
                  .join(', ')}
              </Text>
            </View>
          ) : (
            <TouchableOpacity
              onPress={() => setEditingAddress(true)}
              style={s.addAddressBtn}
            >
              <Ionicons name="add-circle-outline" size={20} color="#D4AF37" />
              <Text style={s.addAddressText}>Add shipping address</Text>
            </TouchableOpacity>
          )}
        </View>

        {/* Payment Token */}
        <View style={s.card}>
          <View style={s.cardHeader}>
            <View style={s.cardIconWrap}>
              <Ionicons name="wallet" size={16} color="#D4AF37" />
            </View>
            <Text style={s.cardTitle}>Payment Method</Text>
          </View>
          <View style={s.tokenGrid}>
            {SUPPORTED_TOKENS.map((tk) => {
              const meta = TOKEN_META[tk];
              const active = selectedToken === tk;
              return (
                <TouchableOpacity
                  key={tk}
                  style={[s.tokenCard, active && s.tokenCardActive]}
                  onPress={() => setSelectedToken(tk)}
                  activeOpacity={0.7}
                >
                  <View
                    style={[
                      s.tokenIcon,
                      {
                        backgroundColor: active ? meta.color + '15' : '#F5F5F5',
                      },
                    ]}
                  >
                    <Ionicons
                      name={meta.icon as any}
                      size={20}
                      color={active ? meta.color : '#999'}
                    />
                  </View>
                  <Text style={[s.tokenName, active && { color: meta.color }]}>
                    {tk}
                  </Text>
                  <Text style={s.tokenSub}>{meta.label}</Text>
                  {active && (
                    <View
                      style={[s.tokenCheck, { backgroundColor: meta.color }]}
                    >
                      <Ionicons name="checkmark" size={12} color="#FFF" />
                    </View>
                  )}
                </TouchableOpacity>
              );
            })}
          </View>
          {isMCGP ? (
            <View style={s.tokenBanner}>
              <Ionicons name="gift-outline" size={14} color="#16A34A" />
              <Text style={s.tokenBannerText}>MCGP: No gas fee applied</Text>
            </View>
          ) : feeConfig?.buyerCashbackPercent ? (
            <View style={s.tokenBanner}>
              <Ionicons name="gift-outline" size={14} color="#16A34A" />
              <Text style={s.tokenBannerText}>
                {feeConfig.buyerCashbackPercent}% cashback on order confirmation
              </Text>
            </View>
          ) : null}
        </View>

        {/* Order Items */}
        <View style={s.card}>
          <View style={s.cardHeader}>
            <View style={s.cardIconWrap}>
              <Ionicons name="bag" size={16} color="#D4AF37" />
            </View>
            <Text style={s.cardTitle}>Items ({summary.totalQuantity})</Text>
          </View>
          {items.map((item, index) => {
            const { name, imageUrl } = getProductData(item);
            return (
              <View
                key={item._id || index}
                style={[s.itemRow, index < items.length - 1 && s.itemBorder]}
              >
                <View style={s.itemImg}>
                  {imageUrl ? (
                    <Image source={{ uri: imageUrl }} style={s.itemImgFull} />
                  ) : (
                    <Ionicons name="cube-outline" size={22} color="#CCC" />
                  )}
                </View>
                <View style={s.itemInfo}>
                  <Text style={s.itemName} numberOfLines={2}>
                    {name}
                  </Text>
                  <Text style={s.itemQty}>Qty: {item.quantity}</Text>
                </View>
                <Text style={s.itemPrice}>
                  ${(item.price * item.quantity).toFixed(2)}
                </Text>
              </View>
            );
          })}
        </View>

        {/* Order Summary */}
        <View style={[s.card, { marginBottom: 100 }]}>
          <View style={s.cardHeader}>
            <View style={s.cardIconWrap}>
              <Ionicons name="receipt" size={16} color="#D4AF37" />
            </View>
            <Text style={s.cardTitle}>Summary</Text>
          </View>

          <View style={s.summaryRow}>
            <Text style={s.summaryLabel}>Subtotal</Text>
            <Text style={s.summaryValue}>${summary.subtotal.toFixed(2)}</Text>
          </View>
          <View style={s.summaryRow}>
            <Text style={s.summaryLabel}>Shipping</Text>
            <Text style={s.summaryValue}>
              {summary.shipping > 0
                ? `$${summary.shipping.toFixed(2)}`
                : 'Free'}
            </Text>
          </View>
          {!isMCGP && gasFee > 0 && (
            <View style={s.summaryRow}>
              <Text style={s.summaryLabel}>Gas Fee</Text>
              <Text style={s.summaryValue}>${gasFee.toFixed(2)}</Text>
            </View>
          )}
          {summary.discount > 0 && (
            <View style={s.summaryRow}>
              <Text style={s.summaryLabel}>Discount</Text>
              <Text style={[s.summaryValue, { color: '#16A34A' }]}>
                -${summary.discount.toFixed(2)}
              </Text>
            </View>
          )}

          <View style={s.summaryDivider} />

          <View style={s.totalRow}>
            <Text style={s.totalLabel}>Total</Text>
            <View style={{ alignItems: 'flex-end' }}>
              <Text style={s.totalValue}>${estimatedTotal.toFixed(2)}</Text>
              <Text style={s.totalToken}>Paid in {selectedToken}</Text>
            </View>
          </View>
        </View>
      </ScrollView>

      {/* Fixed Footer */}
      <View style={s.footer}>
        <View style={s.footerRow}>
          <View>
            <Text style={s.footerLabel}>Total</Text>
            <Text style={s.footerTotal}>${estimatedTotal.toFixed(2)}</Text>
          </View>
          <TouchableOpacity
            style={s.payBtn}
            activeOpacity={0.8}
            onPress={handleCreateOrders}
          >
            <Ionicons name="shield-checkmark" size={18} color="#FFF" />
            <Text style={s.payBtnText}>Pay with {selectedToken}</Text>
          </TouchableOpacity>
        </View>
      </View>
    </SafeAreaView>
  );
};

const s = StyleSheet.create({
  safe: { flex: 1, backgroundColor: '#F8F9FA' },
  center: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 24,
    gap: 12,
  },
  mutedText: { fontSize: 14, color: '#888', textAlign: 'center' },

  // Header
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: '#FFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  backBtn: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: '#F5F5F5',
    justifyContent: 'center',
    alignItems: 'center',
  },
  headerTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },

  // Steps
  steps: {
    flexDirection: 'row',
    justifyContent: 'center',
    gap: 32,
    paddingVertical: 16,
  },
  stepItem: { alignItems: 'center', gap: 4 },
  stepDot: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: '#E8E8E8',
    justifyContent: 'center',
    alignItems: 'center',
  },
  stepDotActive: { backgroundColor: '#D4AF37' },
  stepDotText: { fontSize: 12, fontWeight: '700', color: '#999' },
  stepDotTextActive: { color: '#FFF' },
  stepLabel: { fontSize: 11, color: '#999', fontWeight: '500' },
  stepLabelActive: { color: '#D4AF37', fontWeight: '600' },

  scroll: { paddingHorizontal: 16, paddingTop: 4 },

  // Card
  card: {
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 16,
    marginBottom: 12,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 1,
  },
  cardHeader: { flexDirection: 'row', alignItems: 'center', marginBottom: 14 },
  cardIconWrap: {
    width: 28,
    height: 28,
    borderRadius: 8,
    backgroundColor: 'rgba(212,175,55,0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 10,
  },
  cardTitle: { fontSize: 15, fontWeight: '600', color: '#1A1A1A', flex: 1 },
  editLink: { fontSize: 13, fontWeight: '600', color: '#D4AF37' },

  // Address
  addressContent: { paddingLeft: 38 },
  addressName: {
    fontSize: 15,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 2,
  },
  addressLine: { fontSize: 13, color: '#888', lineHeight: 18 },
  addAddressBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    paddingLeft: 38,
    paddingVertical: 8,
  },
  addAddressText: { fontSize: 14, fontWeight: '600', color: '#D4AF37' },

  // Token
  tokenGrid: { flexDirection: 'row', gap: 10 },
  tokenCard: {
    flex: 1,
    alignItems: 'center',
    paddingVertical: 14,
    paddingHorizontal: 8,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: '#F0F0F0',
    backgroundColor: '#FAFAFA',
    gap: 4,
  },
  tokenCardActive: { borderColor: '#D4AF37', backgroundColor: '#FFFDF7' },
  tokenIcon: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
  },
  tokenName: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  tokenSub: { fontSize: 10, color: '#AAA' },
  tokenCheck: {
    position: 'absolute',
    top: 6,
    right: 6,
    width: 18,
    height: 18,
    borderRadius: 9,
    justifyContent: 'center',
    alignItems: 'center',
  },
  tokenBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    marginTop: 12,
    paddingVertical: 8,
    paddingHorizontal: 12,
    backgroundColor: '#F0FDF4',
    borderRadius: 8,
  },
  tokenBannerText: { fontSize: 12, color: '#16A34A', fontWeight: '500' },

  // Items
  itemRow: { flexDirection: 'row', alignItems: 'center', paddingVertical: 10 },
  itemBorder: { borderBottomWidth: 1, borderBottomColor: '#F5F5F5' },
  itemImg: {
    width: 48,
    height: 48,
    borderRadius: 10,
    backgroundColor: '#F5F5F5',
    justifyContent: 'center',
    alignItems: 'center',
    overflow: 'hidden',
  },
  itemImgFull: { width: 48, height: 48, borderRadius: 10 },
  itemInfo: { flex: 1, marginLeft: 12 },
  itemName: {
    fontSize: 14,
    fontWeight: '500',
    color: '#1A1A1A',
    marginBottom: 2,
  },
  itemQty: { fontSize: 12, color: '#AAA' },
  itemPrice: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },

  // Summary
  summaryRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingVertical: 6,
  },
  summaryLabel: { fontSize: 14, color: '#888' },
  summaryValue: { fontSize: 14, fontWeight: '500', color: '#1A1A1A' },
  summaryDivider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 10 },
  totalRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  totalLabel: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  totalValue: { fontSize: 22, fontWeight: '800', color: '#D4AF37' },
  totalToken: { fontSize: 11, color: '#AAA', marginTop: 2 },

  // Footer
  footer: {
    position: 'absolute',
    bottom: 0,
    left: 0,
    right: 0,
    backgroundColor: '#FFF',
    paddingHorizontal: 16,
    paddingTop: 12,
    paddingBottom: Platform.OS === 'ios' ? 28 : 16,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: -2 },
    shadowOpacity: 0.05,
    shadowRadius: 8,
    elevation: 8,
  },
  footerRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  footerLabel: { fontSize: 12, color: '#AAA' },
  footerTotal: { fontSize: 22, fontWeight: '800', color: '#1A1A1A' },
  payBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    backgroundColor: '#D4AF37',
    paddingHorizontal: 24,
    paddingVertical: 14,
    borderRadius: 14,
  },
  payBtnText: { fontSize: 15, fontWeight: '700', color: '#FFF' },

  // Buttons
  primaryBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    backgroundColor: '#D4AF37',
    paddingHorizontal: 28,
    paddingVertical: 14,
    borderRadius: 14,
    marginTop: 8,
  },
  primaryBtnText: { fontSize: 15, fontWeight: '700', color: '#FFF' },
  outlineBtn: {
    paddingHorizontal: 28,
    paddingVertical: 14,
    borderRadius: 14,
    marginTop: 4,
    borderWidth: 1.5,
    borderColor: '#E8E8E8',
  },
  outlineBtnText: { fontSize: 15, fontWeight: '600', color: '#888' },

  // Error
  errorCircle: {
    width: 64,
    height: 64,
    borderRadius: 32,
    backgroundColor: '#FEE2E2',
    justifyContent: 'center',
    alignItems: 'center',
  },
  errorTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  partialText: { fontSize: 13, color: '#888' },

  // Success
  successCircle: {
    width: 72,
    height: 72,
    borderRadius: 36,
    backgroundColor: '#16A34A',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 8,
  },
  successTitle: { fontSize: 22, fontWeight: '700', color: '#1A1A1A' },
  successOrders: { width: '100%', marginTop: 12, gap: 8 },
  successOrderRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    backgroundColor: '#FFF',
    padding: 14,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  successOrderId: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  successOrderToken: { fontSize: 12, color: '#AAA', marginTop: 2 },
  successOrderAmount: { fontSize: 16, fontWeight: '700', color: '#D4AF37' },

  // Processing
  processingRing: { marginBottom: 12 },
  processingTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: '#1A1A1A',
    textAlign: 'center',
  },
  progressBar: {
    width: '70%',
    height: 4,
    backgroundColor: '#E8E8E8',
    borderRadius: 2,
    marginTop: 16,
    overflow: 'hidden',
  },
  progressFill: { height: '100%', backgroundColor: '#D4AF37', borderRadius: 2 },
});

export default CheckoutScreen;
