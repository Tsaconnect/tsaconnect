// app/(dashboard)/(tabs)/(home)/checkout/index.tsx
//
// Checkout (home tab) — review cart, pick shipping address + payment token, then
// run the escrow checkout flow (createOrders → approve → escrow → submit) signing
// + broadcasting client-side. Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: every order total, shipping/address handling, fee/gas math,
// and the place-order escrow sign/broadcast/confirm flow is preserved verbatim —
// this is a visual/structural restyle only. The reused LocationPicker (already on
// the design system) is unchanged.
import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  ScrollView,
  Alert,
  Image,
  Platform,
  KeyboardAvoidingView,
} from 'react-native';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '@/AuthContext/AuthContext';
import {
  cartService,
  CartSummaryResponse,
  CartItem,
  Product,
} from '@/components/services/cart';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import {
  createOrders,
  prepareApprove,
  prepareEscrow,
  submitEscrow,
  formatTokenAmount,
  getShippingEstimate,
  Order,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import { decodeServiceNotes } from '@/components/services/serviceCart';
import LocationPicker from '@/components/common/LocationPicker';
import { getActiveWallet } from '@/services/wallet';
import { useKycVerification } from '@/hooks/useKycVerification';
import { useTheme } from '@/theme';
import { MARKETPLACE_TAB } from '@/constants/routes';
import {
  AccentCard,
  AmountText,
  Button,
  Card,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  SummaryRow,
  Text,
} from '@/components/ui';

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

// Brand-identity colors for each stablecoin/token (used only to tint the token
// icon chip so the selector reads as the real coin). These are token identity,
// not UI theme — all surface/text chrome comes from useTheme tokens below.
const TOKEN_META: Record<
  string,
  { icon: string; color: string; label: string }
> = {
  USDC: { icon: 'card', color: '#2775CA', label: 'USD Coin' },
  USDT: { icon: 'card', color: '#26A17B', label: 'Tether' },
  MCGP: { icon: 'pricetag', color: '#D4AF37', label: 'MCGP (0% fee)' },
};

const CheckoutScreen = () => {
  const { colors, spacing, radius } = useTheme();
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
  const [shippingEstimateTotal, setShippingEstimateTotal] = useState<number | null>(null);
  const [shippingEstimateLoading, setShippingEstimateLoading] = useState(false);
  const { currency, rate } = useCurrency();

  const fmtPrice = (usd: number) => formatPriceUtil(usd, { currency, rate });

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
    // Check if wallet exists
    (async () => {
      const addr = await getActiveWallet();
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

  const getProductId = (item: CartItem) => {
    if (typeof item.product === 'object') {
      return (item.product as Product)._id;
    }

    return typeof item.product === 'string' ? item.product : '';
  };

  const shippingName = currentUser?.name || currentUser?.username || '';
  const shippingAddress = {
    city: shippingLocation.city,
    state: shippingLocation.state,
    country: shippingLocation.country,
  };

  // --- Escrow checkout flow ---
  const handleCreateOrders = async () => {
    if (!requireKycVerified()) return;
    // Re-check wallet in case user came back without completing setup
    const addr = await getActiveWallet();
    if (!addr) {
      Alert.alert(
        'Wallet Required',
        'Please set up your wallet first.',
        [{ text: 'Set Up Wallet', onPress: () => router.push('/tokenization') }],
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
    if (shippingEstimateLoading) {
      Alert.alert(
        'Updating Shipping',
        'Please wait for shipping fees to finish updating.',
      );
      return;
    }
    if (!cartData || items.length === 0) {
      Alert.alert('Empty Cart', 'Add items to your cart before checking out.');
      return;
    }

    setStep('signing');
    setSigningPhase('creating_orders');
    setSigningError(null);
    setCompletedOrders([]);

    try {
      // Build the per-service declared-tier map (productId → tier) from the
      // service line items in the cart. Plain product items carry no service
      // note and are skipped. This threads the buyer's residential/booking
      // declaration through to the backend (spec §6.5, "quote == charge").
      const serviceTiers: Record<string, string> = {};
      for (const item of items) {
        const meta = decodeServiceNotes(item.notes);
        if (meta) {
          serviceTiers[getProductId(item)] = meta.tier;
        }
      }

      const createResult = await createOrders(
        selectedToken,
        shippingLocation.city,
        shippingLocation.state,
        shippingLocation.country,
        Object.keys(serviceTiers).length > 0 ? serviceTiers : undefined,
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
          approveTxHash = await signAndBroadcast(approveResult.data.approveTx, 'sonic');
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
          escrowTxHash = await signAndBroadcast(escrowResult.data.createOrderTx, 'sonic');
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
      // Checkout goes through orderApi (not cartService), and the backend marks
      // the cart converted — nudge the shared count badge so it drops to 0.
      cartService.notifyChange();
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
  const items = cartData?.itemsBySeller?.flatMap((group) => group.items) || cartData?.cart.items || [];
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
  // Never fabricate a gas fee: fall back to 0 (not a hardcoded estimate) so the
  // total stays honest if the backend omits the fee. The Gas Fee row only shows
  // when there's a real positive value.
  const gasFee = isMCGP ? 0 : (summary.gasFee || feeConfig?.gasFeeUSD || 0);

  useEffect(() => {
    let cancelled = false;

    const loadShippingEstimate = async () => {
      const checkoutItems =
        cartData?.itemsBySeller?.flatMap((group) => group.items) ||
        cartData?.cart.items ||
        [];

      if (checkoutItems.length === 0) {
        setShippingEstimateTotal(0);
        setShippingEstimateLoading(false);
        return;
      }

      if (!shippingLocation.country || !shippingLocation.state) {
        setShippingEstimateTotal(null);
        setShippingEstimateLoading(false);
        return;
      }

      setShippingEstimateLoading(true);

      try {
        const estimates = await Promise.all(
          checkoutItems.map(async (item) => {
            const productId = getProductId(item);
            if (!productId) {
              return null;
            }

            const result = await getShippingEstimate(
              productId,
              shippingLocation.city,
              shippingLocation.state,
              shippingLocation.country,
            );

            if (!result.success || result.data?.shippingCost == null) {
              return null;
            }

            return result.data.shippingCost * item.quantity;
          }),
        );

        if (cancelled) {
          return;
        }

        const resolvedEstimates = estimates.filter(
          (value): value is number => value != null,
        );

        if (resolvedEstimates.length !== estimates.length) {
          setShippingEstimateTotal(null);
          return;
        }

        setShippingEstimateTotal(
          resolvedEstimates.reduce((total, value) => total + value, 0),
        );
      } catch (err) {
        if (!cancelled) {
          console.error('Failed to load shipping estimates:', err);
          setShippingEstimateTotal(null);
        }
      } finally {
        if (!cancelled) {
          setShippingEstimateLoading(false);
        }
      }
    };

    loadShippingEstimate();

    return () => {
      cancelled = true;
    };
  }, [
    cartData,
    shippingLocation.city,
    shippingLocation.state,
    shippingLocation.country,
  ]);

  // Service line items charge a "Call-out fee", not "Shipping". Relabel the
  // aggregated fee row accordingly (mixed carts read "Shipping & call-out").
  const hasServiceItem = items.some((it) => decodeServiceNotes(it.notes) != null);
  const hasProductItem = items.some((it) => decodeServiceNotes(it.notes) == null);
  const feeRowLabel = hasServiceItem
    ? hasProductItem
      ? 'Shipping & call-out'
      : 'Call-out fee'
    : 'Shipping';

  const shippingTotal = shippingEstimateTotal ?? summary.shipping;
  const estimatedTotal =
    summary.subtotal + shippingTotal + gasFee - summary.discount;
  const totalDisplay = shippingEstimateLoading
    ? 'Updating...'
    : fmtPrice(estimatedTotal);

  // ===== HEADER =====
  const Header = ({
    title = 'Checkout',
    showBack = true,
  }: {
    title?: string;
    showBack?: boolean;
  }) => (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.sm,
        borderBottomWidth: 1,
        borderBottomColor: colors.hairline,
        backgroundColor: colors.surface,
      }}
    >
      {showBack ? (
        <IconButton
          icon="chevronLeft"
          accessibilityLabel="Go back"
          variant="surface"
          onPress={() => router.back()}
        />
      ) : (
        <View style={{ width: 44 }} />
      )}
      <Text variant="title3" weight="700">
        {title}
      </Text>
      <View style={{ width: 44 }} />
    </View>
  );

  // ===== LOADING =====
  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header />
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading your order..." />
        </View>
      </SafeAreaView>
    );
  }

  // ===== ERROR =====
  if (error && step === 'review') {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <ErrorState subtitle={error} onRetry={loadCart} />
        </View>
      </SafeAreaView>
    );
  }

  // ===== EMPTY CART =====
  if (items.length === 0 && step === 'review') {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="cart"
            title="Your cart is empty"
            actionLabel="Start Shopping"
            onAction={() => router.replace(MARKETPLACE_TAB)}
          />
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
        { text: 'Set Up Wallet', onPress: () => router.push('/tokenization') },
      ],
    );
    // Reset so the alert doesn't re-trigger
    setHasWallet(null);
  }

  // ===== SUCCESS =====
  if (step === 'success') {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <ScrollView
          contentContainerStyle={{
            flexGrow: 1,
            justifyContent: 'center',
            padding: spacing.lg,
            paddingBottom: spacing['4xl'],
          }}
        >
          <View style={{ alignItems: 'center', gap: spacing.sm }}>
            <View
              style={{
                width: 72,
                height: 72,
                borderRadius: radius.pill,
                backgroundColor: colors.success,
                justifyContent: 'center',
                alignItems: 'center',
                marginBottom: spacing.sm,
              }}
            >
              <Icon name="check" size={40} color={colors.textInverse} />
            </View>
            <Text variant="title1" weight="700" align="center">
              Payment Successful!
            </Text>
            <Text variant="bodySm" color="textSecondary" align="center">
              {orders.length} order{orders.length !== 1 ? 's' : ''} created &
              funded via escrow
            </Text>
          </View>

          <View style={{ width: '100%', marginTop: spacing.lg, gap: spacing.sm }}>
            {orders.map((order) => (
              <Card key={order.id} padding="md">
                <View
                  style={{
                    flexDirection: 'row',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                  }}
                >
                  <View>
                    <Text variant="bodySm" weight="600">
                      #{order.id.slice(0, 8)}
                    </Text>
                    <Text variant="caption" color="textMuted">
                      {order.token}
                    </Text>
                  </View>
                  <AmountText
                    value={formatTokenAmount(order.totalAmount, order.token)}
                    variant="title3"
                    color="accent"
                  />
                </View>
              </Card>
            ))}
          </View>

          <View style={{ marginTop: spacing.xl, gap: spacing.sm }}>
            <Button
              title="View Orders"
              leftIcon="list"
              onPress={() => router.push('/(dashboard)/orderlist')}
            />
            <Button
              title="Continue Shopping"
              variant="secondary"
              onPress={() => router.replace('/home')}
            />
          </View>
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ===== SIGNING (PROCESSING) =====
  if (step === 'signing') {
    const progress =
      orders.length > 0 ? completedOrders.length / orders.length : 0;
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header title="Processing" showBack={false} />
        <View
          style={{
            flex: 1,
            justifyContent: 'center',
            alignItems: 'center',
            paddingHorizontal: spacing['2xl'],
            gap: spacing.md,
          }}
        >
          {signingError ? (
            <>
              <View
                style={{
                  width: 64,
                  height: 64,
                  borderRadius: radius.pill,
                  backgroundColor: colors.danger,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
              >
                <Icon name="close" size={32} color={colors.textInverse} />
              </View>
              <Text variant="title3" weight="700" align="center">
                Transaction Failed
              </Text>
              <Text variant="bodySm" color="textSecondary" align="center">
                {signingError}
              </Text>
              {completedOrders.length > 0 && (
                <Text variant="bodySm" color="textMuted" align="center">
                  {completedOrders.length}/{orders.length} orders completed
                </Text>
              )}
              <Button
                title={
                  completedOrders.length > 0 ? 'View Orders' : 'Back to Checkout'
                }
                fullWidth={false}
                style={{ marginTop: spacing.sm }}
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
              />
            </>
          ) : (
            <>
              <Spinner size="large" />
              <Text variant="title3" weight="600" align="center">
                {getSigningStatusText()}
              </Text>
              {orders.length > 1 && (
                <View
                  style={{
                    width: '70%',
                    height: 4,
                    backgroundColor: colors.surfaceAlt,
                    borderRadius: radius.pill,
                    marginTop: spacing.lg,
                    overflow: 'hidden',
                  }}
                >
                  <View
                    style={{
                      height: '100%',
                      width: `${progress * 100}%`,
                      backgroundColor: colors.accent,
                      borderRadius: radius.pill,
                    }}
                  />
                </View>
              )}
              <Text variant="bodySm" color="textSecondary" align="center">
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Header />

      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 0 : 20}
      >
        <ScrollView
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          {/* Step Indicators */}
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'center',
              gap: spacing['3xl'],
              marginBottom: spacing.lg,
            }}
          >
            {['Review', 'Pay', 'Done'].map((label, i) => {
              const active = i === 0;
              return (
                <View key={label} style={{ alignItems: 'center', gap: spacing.xs }}>
                  <View
                    style={{
                      width: 28,
                      height: 28,
                      borderRadius: radius.pill,
                      backgroundColor: active ? colors.accent : colors.surfaceAlt,
                      justifyContent: 'center',
                      alignItems: 'center',
                    }}
                  >
                    <Text
                      variant="caption"
                      weight="700"
                      style={{ color: active ? colors.onAccent : colors.textMuted }}
                    >
                      {i + 1}
                    </Text>
                  </View>
                  <Text
                    variant="caption"
                    weight={active ? '600' : '500'}
                    color={active ? 'accent' : 'textMuted'}
                  >
                    {label}
                  </Text>
                </View>
              );
            })}
          </View>

          {/* Shipping Address */}
          <Card style={{ marginBottom: spacing.md }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                marginBottom: spacing.md,
              }}
            >
              <Icon name="nearMe" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
                Shipping Address
              </Text>
              <PressableScale onPress={() => setEditingAddress(!editingAddress)} hitSlop={8}>
                <Text variant="bodySm" weight="600" color="accent">
                  {editingAddress ? 'Done' : 'Edit'}
                </Text>
              </PressableScale>
            </View>
            {editingAddress ? (
              <LocationPicker
                value={shippingLocation}
                onChange={setShippingLocation}
              />
            ) : shippingAddress.country ? (
              <View>
                <Text variant="bodyMed" weight="600">
                  {shippingName}
                </Text>
                <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
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
              <PressableScale
                onPress={() => setEditingAddress(true)}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                  paddingVertical: spacing.sm,
                }}
              >
                <Icon name="plus" size={18} color={colors.accent} />
                <Text variant="bodySm" weight="600" color="accent">
                  Add shipping address
                </Text>
              </PressableScale>
            )}
          </Card>

          {/* Payment Token */}
          <Card style={{ marginBottom: spacing.md }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                marginBottom: spacing.md,
              }}
            >
              <Icon name="wallet" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="600">
                Payment Method
              </Text>
            </View>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              {SUPPORTED_TOKENS.map((tk) => {
                const meta = TOKEN_META[tk];
                const active = selectedToken === tk;
                return (
                  <PressableScale
                    key={tk}
                    onPress={() => setSelectedToken(tk)}
                    style={{
                      flex: 1,
                      alignItems: 'center',
                      paddingVertical: spacing.md,
                      paddingHorizontal: spacing.sm,
                      borderRadius: radius.md,
                      borderWidth: 1.5,
                      borderColor: active ? colors.accent : colors.border,
                      backgroundColor: active ? colors.accentMuted : colors.surfaceAlt,
                      gap: spacing.xs,
                    }}
                  >
                    <View
                      style={{
                        width: 36,
                        height: 36,
                        borderRadius: radius.pill,
                        justifyContent: 'center',
                        alignItems: 'center',
                        backgroundColor: active ? meta.color + '22' : colors.surfaceAlt,
                      }}
                    >
                      <Icon
                        name={meta.icon as any}
                        size={20}
                        color={active ? meta.color : colors.textMuted}
                      />
                    </View>
                    <Text
                      variant="bodySm"
                      weight="700"
                      style={active ? { color: meta.color } : undefined}
                    >
                      {tk}
                    </Text>
                    <Text variant="caption" color="textMuted" align="center">
                      {meta.label}
                    </Text>
                    {active && (
                      <View
                        style={{
                          position: 'absolute',
                          top: spacing.xs,
                          right: spacing.xs,
                          width: 18,
                          height: 18,
                          borderRadius: radius.pill,
                          justifyContent: 'center',
                          alignItems: 'center',
                          backgroundColor: meta.color,
                        }}
                      >
                        <Icon name="check" size={12} color="#FFFFFF" />
                      </View>
                    )}
                  </PressableScale>
                );
              })}
            </View>
            {isMCGP ? (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  marginTop: spacing.md,
                  paddingVertical: spacing.sm,
                  paddingHorizontal: spacing.md,
                  backgroundColor: colors.successBg,
                  borderRadius: radius.sm,
                }}
              >
                <Icon name="star" size={14} color={colors.success} />
                <Text variant="caption" weight="500" color="success">
                  MCGP: No gas fee applied
                </Text>
              </View>
            ) : feeConfig?.buyerCashbackPercent ? (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  marginTop: spacing.md,
                  paddingVertical: spacing.sm,
                  paddingHorizontal: spacing.md,
                  backgroundColor: colors.successBg,
                  borderRadius: radius.sm,
                }}
              >
                <Icon name="star" size={14} color={colors.success} />
                <Text variant="caption" weight="500" color="success">
                  {feeConfig.buyerCashbackPercent}% cashback on order confirmation
                </Text>
              </View>
            ) : null}
          </Card>

          {/* Order Items */}
          <Card style={{ marginBottom: spacing.md }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                marginBottom: spacing.md,
              }}
            >
              <Icon name="cart" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="600">
                Items ({summary.totalQuantity})
              </Text>
            </View>
            {items.map((item, index) => {
              const { name, imageUrl } = getProductData(item);
              return (
                <View key={item._id || index}>
                  {index > 0 && <Divider spacing="sm" />}
                  <View
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      paddingVertical: spacing.xs,
                    }}
                  >
                    <View
                      style={{
                        width: 48,
                        height: 48,
                        borderRadius: radius.md,
                        backgroundColor: colors.surfaceAlt,
                        justifyContent: 'center',
                        alignItems: 'center',
                        overflow: 'hidden',
                      }}
                    >
                      {imageUrl ? (
                        <Image
                          source={{ uri: imageUrl }}
                          style={{ width: 48, height: 48, borderRadius: radius.md }}
                        />
                      ) : (
                        <Icon name="cart" size={22} color={colors.textMuted} />
                      )}
                    </View>
                    <View style={{ flex: 1, marginLeft: spacing.md }}>
                      <Text variant="bodySm" weight="500" numberOfLines={2}>
                        {name}
                      </Text>
                      <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                        Qty: {item.quantity}
                      </Text>
                    </View>
                    <Text variant="bodyMed" weight="700" numeric>
                      {fmtPrice(item.price * item.quantity)}
                    </Text>
                  </View>
                </View>
              );
            })}
          </Card>

          {/* Order Summary */}
          <Card>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                marginBottom: spacing.sm,
              }}
            >
              <Icon name="list" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="600">
                Summary
              </Text>
            </View>

            <SummaryRow label="Subtotal" value={fmtPrice(summary.subtotal)} />
            <SummaryRow
              label={feeRowLabel}
              value={
                shippingEstimateLoading
                  ? 'Calculating...'
                  : shippingTotal > 0
                  ? fmtPrice(shippingTotal)
                  : 'Free'
              }
            />
            {!isMCGP && gasFee > 0 && (
              <SummaryRow label="Gas Fee" value={fmtPrice(gasFee)} />
            )}
            {summary.discount > 0 && (
              <SummaryRow
                label="Discount"
                value={`-${fmtPrice(summary.discount)}`}
                valueColor="success"
              />
            )}

            {feeConfig?.merchantFeePercent != null && feeConfig.merchantFeePercent > 0 && (
              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  paddingVertical: spacing.sm,
                }}
              >
                <Text variant="bodySm" color="textMuted">
                  Platform Fee ({feeConfig.merchantFeePercent}%)
                </Text>
                <Text variant="bodySm" color="textMuted">
                  Charged to seller
                </Text>
              </View>
            )}

            <Divider spacing="sm" />

            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
              }}
            >
              <Text variant="title3" weight="700">
                Total
              </Text>
              <View style={{ alignItems: 'flex-end' }}>
                <Text variant="title1" weight="800" color="accent" numeric>
                  {totalDisplay}
                </Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                  Paid in {selectedToken}
                </Text>
              </View>
            </View>
          </Card>
        </ScrollView>
      </KeyboardAvoidingView>

      {/* Fixed Footer */}
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: spacing.md,
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: Platform.OS === 'ios' ? spacing['3xl'] : spacing.lg,
          backgroundColor: colors.surface,
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
        }}
      >
        <View>
          <Text variant="caption" color="textMuted">
            Total
          </Text>
          <Text variant="title1" weight="800" numeric>
            {totalDisplay}
          </Text>
        </View>
        <Button
          title={`Pay with ${selectedToken}`}
          leftIcon="shield"
          fullWidth={false}
          disabled={shippingEstimateLoading}
          onPress={handleCreateOrders}
        />
      </View>
    </SafeAreaView>
  );
};

export default CheckoutScreen;
