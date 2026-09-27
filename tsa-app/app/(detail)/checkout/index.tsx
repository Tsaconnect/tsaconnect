// app/(detail)/checkout/index.tsx
//
// Checkout — review the cart, pick a delivery address and pay token, confirm
// the exact amount in a sheet, then run the escrow flow (createOrders → approve
// → escrow → submit), signing and broadcasting client-side.
//
// FINANCIAL screen. Every amount shown comes from the backend cart quote
// (POST /orders/quote), which is priced by the same code that creates the
// orders — so the total a buyer confirms is the total the wallet is asked to
// approve. The escrow sign/broadcast/submit sequence itself is unchanged.
import React, { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import { View, ScrollView, Alert, Image } from 'react-native';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '@/AuthContext/AuthContext';
import { cartService, CartSummaryResponse, CartItem, Product } from '@/components/services/cart';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import {
  createOrders,
  Order,
  type CartQuote,
} from '@/services/orderApi';
import { escrowOrder, OrderNotPayableError, type EscrowProgress } from '@/services/serviceCheckout';
import { decodeServiceNotes } from '@/components/services/serviceCart';
import { getActiveWallet } from '@/services/wallet';
import { useKycVerification } from '@/hooks/useKycVerification';
import { listingAmount, quoteUnits, useCheckoutQuote, useQuoteFormat } from '@/hooks/useCheckoutQuote';
import { useCheckoutBalances } from '@/hooks/useCheckoutBalances';
import { ConfirmPaymentSheet } from '@/components/checkout/ConfirmPaymentSheet';
import { DeliveryAddressSheet } from '@/components/checkout/DeliveryAddressSheet';
import { useDeliveryAddresses } from '@/hooks/useDeliveryAddresses';
import { formatDeliveryAddress } from '@/services/deliveryAddressApi';
import { PaymentProgress, type PaymentPhase } from '@/components/checkout/PaymentProgress';
import { QuoteBreakdown } from '@/components/checkout/QuoteBreakdown';
import { useTheme } from '@/theme';
import { MARKETPLACE_TAB } from '@/constants/routes';
import {
  BottomActionBar,
  Button,
  Card,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

type CheckoutStep = 'review' | 'signing' | 'success';

const SUPPORTED_TOKENS = ['USDC', 'USDT', 'MCGP'] as const;

// Brand-identity colors for each stablecoin/token (used only to tint the token
// icon chip so the selector reads as the real coin). These are token identity,
// not UI theme — all surface/text chrome comes from useTheme tokens below.
const TOKEN_META: Record<string, { icon: string; color: string }> = {
  USDC: { icon: 'card', color: '#2775CA' },
  USDT: { icon: 'card', color: '#26A17B' },
  MCGP: { icon: 'pricetag', color: '#D4AF37' },
};

const formatBalance = (n: number) => n.toLocaleString('en-US', { maximumFractionDigits: 2 });

const CheckoutScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { currentUser, token } = useAuth();
  const { requireKycVerified } = useKycVerification();
  const [cartData, setCartData] = useState<CartSummaryResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [step, setStep] = useState<CheckoutStep>('review');
  const [selectedToken, setSelectedToken] = useState<string>('USDC');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [signingPhase, setSigningPhase] = useState<PaymentPhase>('idle');
  const [currentOrderIndex, setCurrentOrderIndex] = useState(0);
  const [orders, setOrders] = useState<Order[]>([]);
  const [completedOrders, setCompletedOrders] = useState<string[]>([]);
  const [signingError, setSigningError] = useState<string | null>(null);
  // The quote the buyer confirmed; progress and the receipt read from it so
  // they show what was agreed even if the live quote re-prices meanwhile.
  const [paidQuote, setPaidQuote] = useState<CartQuote | null>(null);
  // Set when order creation reported a different total than the one confirmed:
  // the confirm sheet reopens on the fresh quote.
  const [repriced, setRepriced] = useState(false);
  const [hasWallet, setHasWallet] = useState<boolean | null>(null);
  const [addressSheetOpen, setAddressSheetOpen] = useState(false);
  const {
    addresses,
    selected: deliveryAddress,
    select: selectAddress,
    loading: addressesLoading,
    error: addressesError,
    reload: reloadAddresses,
  } = useDeliveryAddresses();
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
      // The Go API returns `id`; `_id` is the legacy field name.
      const product = item.product as Product & { id?: string };
      return product.id || product._id;
    }
    return typeof item.product === 'string' ? item.product : '';
  };

  // --- Derived data ---
  const items = useMemo(
    () => cartData?.itemsBySeller?.flatMap((group) => group.items) || cartData?.cart.items || [],
    [cartData],
  );
  const totalQuantity = cartData?.summary?.totalQuantity ?? items.reduce((n, it) => n + it.quantity, 0);
  const feeConfig = cartData?.feeConfig;
  const isMCGP = selectedToken === 'MCGP';
  // Service line items charge a "Call-out fee", not "Shipping". Relabel the
  // aggregated fee row accordingly (mixed carts read "Shipping & call-out").
  const hasServiceItem = items.some((it) => decodeServiceNotes(it.notes) != null);
  const hasProductItem = items.some((it) => decodeServiceNotes(it.notes) == null);
  const feeRowLabel = hasServiceItem ? (hasProductItem ? 'Shipping & call-out' : 'Call-out fee') : 'Shipping';

  // Physical products ship to a saved address (the seller needs the street and
  // phone). A services-only cart only needs a location to price the call-out,
  // so it can still fall back to the profile location.
  const profileLocation =
    currentUser?.country && currentUser?.state
      ? { city: currentUser.city || '', state: currentUser.state, country: currentUser.country }
      : null;
  const location = deliveryAddress ?? (hasProductItem ? null : profileLocation);
  const hasAddress = !!location;

  // Per-service declared-tier map (productId → tier) from the service line items.
  // Plain product items carry no service note and are skipped. This threads the
  // buyer's residential/booking declaration to the backend (spec §6.5,
  // "quote == charge") for both the quote and the order.
  const serviceTiers = useMemo(() => {
    const tiers: Record<string, string> = {};
    for (const item of items) {
      const meta = decodeServiceNotes(item.notes);
      if (meta) tiers[getProductId(item)] = meta.tier;
    }
    return Object.keys(tiers).length > 0 ? tiers : undefined;
  }, [items]);

  const cartKey = useMemo(
    () => items.map((it) => `${getProductId(it)}:${it.quantity}`).join(','),
    [items],
  );

  const {
    quote,
    loading: quoteLoading,
    error: quoteError,
    refresh: refreshQuote,
  } = useCheckoutQuote({
    token: selectedToken,
    city: location?.city ?? '',
    state: location?.state ?? '',
    country: location?.country ?? '',
    serviceTiers,
    enabled: hasAddress && items.length > 0 && step === 'review',
    cartKey,
  });
  const fmt = useQuoteFormat(quote);
  const paidFmt = useQuoteFormat(paidQuote);
  const { balances, address: walletAddress, refresh: refreshBalances } = useCheckoutBalances();

  const quoteByProduct = useMemo(() => {
    const map = new Map<string, NonNullable<typeof quote>['lines'][number]>();
    for (const line of quote?.lines ?? []) map.set(line.productId, line);
    return map;
  }, [quote]);

  // After a price_changed refusal, reopen the confirm sheet once the re-priced
  // quote has arrived.
  useEffect(() => {
    if (repriced && quote && !quoteLoading) {
      setRepriced(false);
      setConfirmOpen(true);
    }
  }, [repriced, quote, quoteLoading]);

  const tokenBalance = balances[selectedToken as 'USDC' | 'USDT' | 'MCGP'];
  const totalUnits = quote ? quoteUnits(quote.totals.total, quote.decimals) : null;
  const shortOnToken = tokenBalance != null && totalUnits != null && tokenBalance < totalUnits;

  // --- Step 1 → 2: validate, then show the confirm sheet ---
  const openConfirm = async () => {
    if (!requireKycVerified()) return;
    // Re-check wallet in case user came back without completing setup
    const addr = await getActiveWallet();
    if (!addr) {
      Alert.alert('Wallet Required', 'Please set up your wallet first.', [
        { text: 'Set Up Wallet', onPress: () => router.push('/tokenization') },
      ]);
      return;
    }
    if (!location) {
      setAddressSheetOpen(true);
      return;
    }
    if (!cartData || items.length === 0) {
      Alert.alert('Empty Cart', 'Add items to your cart before checking out.');
      return;
    }
    if (!quote) return;
    refreshBalances();
    setConfirmOpen(true);
  };

  // --- Step 2 → 3: the escrow checkout flow ---
  const payNow = async () => {
    if (!quote || !location) return;
    setConfirmOpen(false);
    setPaidQuote(quote);
    setStep('signing');
    setSigningPhase('creating_orders');
    setSigningError(null);
    setCompletedOrders([]);

    let newOrders: Order[];
    try {
      const createResult = await createOrders(
        selectedToken,
        location.city,
        location.state,
        location.country,
        serviceTiers,
        undefined,
        undefined,
        quote.totals.total,
        deliveryAddress?.id,
      );
      if (createResult.code === 'price_changed') {
        // Nothing was created. Back to review, re-price, and show the buyer
        // the new amount before anything is signed.
        setStep('review');
        setSigningPhase('idle');
        setPaidQuote(null);
        setRepriced(true);
        Alert.alert('Price updated', 'The total changed since you confirmed it. Review the new amount before paying.');
        return;
      }
      if (!createResult.success || !createResult.data?.orders) {
        throw new Error(createResult.message || 'Failed to create orders');
      }
      newOrders = createResult.data.orders;
    } catch (err: any) {
      console.error('Checkout create-orders error:', err);
      setSigningError(err.message || 'Something went wrong');
      return;
    }

    orderProgress.current = {};
    setUnpayable([]);
    setOrders(newOrders);
    setCurrentOrderIndex(0);
    await payOrders(newOrders, []);
  };

  // What each created order's payment has already done on-chain, so a retry
  // resumes where it stopped (see EscrowProgress).
  const orderProgress = useRef<Record<string, EscrowProgress>>({});
  // Orders the server says can no longer be paid (cancelled or expired).
  const [unpayable, setUnpayable] = useState<string[]>([]);
  // One payment run at a time: a double tap on "Try again" must not sign twice.
  const paying = useRef(false);

  // Pays each order not skipped through the shared escrow sequence.
  const payOrders = async (list: Order[], skip: string[]) => {
    if (paying.current) return;
    paying.current = true;
    try {
      for (let i = 0; i < list.length; i++) {
        const order = list[i];
        if (skip.includes(order.id)) continue;
        setCurrentOrderIndex(i);
        try {
          await escrowOrder(
            order,
            (phase) => {
              if (phase === 'approving_token' || phase === 'creating_escrow' || phase === 'submitting') {
                setSigningPhase(phase);
              }
            },
            (orderProgress.current[order.id] ??= {}),
          );
        } catch (e) {
          if (e instanceof OrderNotPayableError) setUnpayable((prev) => [...prev, order.id]);
          throw e;
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
    } finally {
      paying.current = false;
    }
  };

  const retryPayment = () => {
    if (paying.current) return;
    setSigningError(null);
    payOrders(orders, [...completedOrders, ...unpayable]);
  };

  // ===== HEADER =====
  const Header = ({ title = 'Checkout', showBack = true }: { title?: string; showBack?: boolean }) => (
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
        <IconButton icon="chevronLeft" accessibilityLabel="Go back" variant="surface" onPress={() => router.back()} />
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
    Alert.alert('Wallet Required', 'You need a wallet to complete your purchase. Set up your wallet to continue.', [
      { text: 'Cancel', style: 'cancel', onPress: () => router.back() },
      { text: 'Set Up Wallet', onPress: () => router.push('/tokenization') },
    ]);
    // Reset so the alert doesn't re-trigger
    setHasWallet(null);
  }

  // ===== SUCCESS (RECEIPT) =====
  if (step === 'success' && paidQuote) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Receipt
          quote={paidQuote}
          orderCount={orders.length}
          shippingLabel={feeRowLabel}
          cashbackPercent={isMCGP ? undefined : feeConfig?.buyerCashbackPercent}
        />
      </SafeAreaView>
    );
  }

  // ===== SIGNING (PROCESSING) =====
  if (step === 'signing' && paidQuote) {
    // Before the orders exist, show the confirmed quote's lines; after, match
    // each created order back to its quote line for a name and amount.
    const lines =
      orders.length > 0
        ? orders.map((o, i) => paidQuote.lines.find((l) => l.productId === o.productId) ?? paidQuote.lines[i])
        : paidQuote.lines;
    const progressOrders = lines.map((line, i) => ({
      name: line?.name ?? `Order ${i + 1}`,
      amount: line ? paidFmt.token(line.total) : '',
    }));
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header title="Paying" showBack={false} />
        <PaymentProgress
          token={paidQuote.token}
          orders={progressOrders}
          phase={signingPhase}
          currentOrderIndex={currentOrderIndex}
          error={signingError}
          completedCount={completedOrders.length}
          onViewOrders={() => router.replace('/(dashboard)/orderlist')}
          ordersCreated={orders.length > 0}
          onRetry={orders.length > completedOrders.length + unpayable.length ? retryPayment : undefined}
          onBackToCheckout={() => {
            setStep('review');
            setSigningError(null);
            setSigningPhase('idle');
            setOrders([]);
            setCompletedOrders([]);
            setUnpayable([]);
            loadCart();
          }}
        />
      </SafeAreaView>
    );
  }

  // ===== REVIEW (MAIN CHECKOUT) =====
  const payDisabled = !quote || quoteLoading || shortOnToken;
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Header />

      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'], gap: spacing.md }}
        keyboardShouldPersistTaps="handled"
      >
        <StepBar active={0} />

        {/* Delivery Address */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
            <Icon name="nearMe" size={16} color={colors.accent} />
            <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
              {hasProductItem ? 'Deliver to' : 'Your location'}
            </Text>
            {deliveryAddress ? (
              <PressableScale onPress={() => setAddressSheetOpen(true)} hitSlop={8}>
                <Text variant="bodySm" weight="600" color="accent">
                  Change
                </Text>
              </PressableScale>
            ) : null}
          </View>
          {addressesError && !deliveryAddress ? (
            <View style={{ gap: spacing.sm }}>
              <Text variant="bodySm" color="danger">
                {addressesError}
              </Text>
              <Button
                title="Try again"
                variant="secondary"
                size="sm"
                fullWidth={false}
                loading={addressesLoading}
                onPress={() => reloadAddresses()}
              />
            </View>
          ) : addressesLoading && !deliveryAddress ? (
            <Spinner label="Loading your addresses…" />
          ) : deliveryAddress ? (
            <View style={{ gap: 2 }}>
              <Text variant="bodyMed" weight="600">
                {deliveryAddress.recipientName}
                {deliveryAddress.label ? (
                  <Text variant="bodySm" color="textMuted">{`  ·  ${deliveryAddress.label}`}</Text>
                ) : null}
              </Text>
              <Text variant="bodySm" color="textSecondary">
                {formatDeliveryAddress(deliveryAddress)}
              </Text>
              <Text variant="caption" color="textMuted">
                {deliveryAddress.phone}
              </Text>
            </View>
          ) : (
            <View>
              {location ? (
                <Text variant="bodySm" color="textSecondary">
                  {[location.city, location.state, location.country].filter(Boolean).join(', ')}
                </Text>
              ) : null}
              <PressableScale
                onPress={() => setAddressSheetOpen(true)}
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, paddingVertical: spacing.sm }}
              >
                <Icon name="plus" size={18} color={colors.accent} />
                <Text variant="bodySm" weight="600" color="accent">
                  {location ? 'Use a saved address instead' : 'Add delivery address'}
                </Text>
              </PressableScale>
            </View>
          )}
        </Card>

        {/* Order Items */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
            <Icon name="cart" size={16} color={colors.accent} />
            <Text variant="bodyMed" weight="600">
              Items ({totalQuantity})
            </Text>
          </View>
          {items.map((item, index) => {
            const { name, imageUrl } = getProductData(item);
            const line = quoteByProduct.get(getProductId(item));
            return (
              <View key={item._id || index}>
                {index > 0 && <Divider spacing="sm" />}
                <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.xs }}>
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
                      <Image source={{ uri: imageUrl }} style={{ width: 48, height: 48, borderRadius: radius.md }} />
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
                  <View style={{ alignItems: 'flex-end' }}>
                    <Text variant="bodyMed" weight="700" numeric>
                      {fmtPrice(item.price * item.quantity)}
                    </Text>
                    {line ? (
                      <Text variant="caption" color="textMuted" numeric>
                        {fmt.token(listingAmount(line))}
                      </Text>
                    ) : null}
                  </View>
                </View>
              </View>
            );
          })}
        </Card>

        {/* Payment Token */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
            <Icon name="wallet" size={16} color={colors.accent} />
            <Text variant="bodyMed" weight="600">
              Pay with
            </Text>
          </View>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            {SUPPORTED_TOKENS.map((tk) => {
              const meta = TOKEN_META[tk];
              const active = selectedToken === tk;
              const bal = balances[tk];
              return (
                <PressableScale
                  key={tk}
                  onPress={() => setSelectedToken(tk)}
                  accessibilityRole="button"
                  accessibilityState={{ selected: active }}
                  accessibilityLabel={`Pay with ${tk}${bal != null ? `, balance ${formatBalance(bal)}` : ''}`}
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
                      width: 32,
                      height: 32,
                      borderRadius: radius.pill,
                      justifyContent: 'center',
                      alignItems: 'center',
                      backgroundColor: active ? meta.color + '22' : colors.surfaceAlt,
                    }}
                  >
                    <Icon name={meta.icon as any} size={18} color={active ? meta.color : colors.textMuted} />
                  </View>
                  <Text variant="bodySm" weight="700" style={active ? { color: meta.color } : undefined}>
                    {tk}
                  </Text>
                  <Text variant="caption" color="textMuted" numeric>
                    {bal != null ? `Bal ${formatBalance(bal)}` : '—'}
                  </Text>
                </PressableScale>
              );
            })}
          </View>
          {shortOnToken ? (
            <Notice tone="danger" icon="info">
              {`Not enough ${selectedToken}. You need ${fmt.token(quote!.totals.total)}; your balance is ${formatBalance(tokenBalance!)} ${selectedToken}.`}
            </Notice>
          ) : isMCGP ? (
            <Notice tone="success" icon="star">
              MCGP: no network or platform fee
            </Notice>
          ) : feeConfig?.buyerCashbackPercent ? (
            <Notice tone="success" icon="star">
              {`${feeConfig.buyerCashbackPercent}% cashback when you confirm delivery`}
            </Notice>
          ) : null}
        </Card>

        {/* Summary */}
        <Card>
          {!hasAddress ? (
            <Text variant="bodySm" color="textSecondary">
              Add a delivery address to see shipping and your total.
            </Text>
          ) : quoteError ? (
            <View style={{ gap: spacing.sm }}>
              <Text variant="bodySm" color="danger">
                {quoteError}
              </Text>
              <Button title="Try again" variant="secondary" size="sm" fullWidth={false} onPress={refreshQuote} />
            </View>
          ) : !quote ? (
            <Spinner label="Pricing your order…" />
          ) : (
            <>
              <QuoteBreakdown quote={quote} shippingLabel={feeRowLabel} />
              <Divider spacing="sm" />
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-end' }}>
                <Text variant="bodyMed" weight="700">
                  You pay
                </Text>
                <View style={{ alignItems: 'flex-end' }}>
                  <Text variant="title1" weight="700" numeric>
                    {fmt.token(quote.totals.total)}
                  </Text>
                  {fmt.approx(quote.totals.total) ? (
                    <Text variant="bodySm" weight="600" color="accent" numeric>
                      {fmt.approx(quote.totals.total)}
                    </Text>
                  ) : null}
                </View>
              </View>
            </>
          )}
        </Card>
      </ScrollView>

      {/* Fixed footer — BottomActionBar keeps the CTA above the system nav bar. */}
      <BottomActionBar style={{ gap: spacing.xs }}>
        {quote ? (
          <Text variant="caption" color="textMuted" align="center" numeric>
            {[fmt.token(quote.totals.total), fmt.approx(quote.totals.total)].filter(Boolean).join(' · ')}
          </Text>
        ) : null}
        <Button title="Review payment" leftIcon="shield" disabled={payDisabled} onPress={openConfirm} />
      </BottomActionBar>

      <DeliveryAddressSheet
        visible={addressSheetOpen}
        onClose={() => setAddressSheetOpen(false)}
        addresses={addresses}
        selectedId={deliveryAddress?.id ?? null}
        onSelect={selectAddress}
        onChanged={reloadAddresses}
        prefill={{
          recipientName: currentUser?.name || '',
          phone: currentUser?.phoneNumber || '',
          country: currentUser?.country || '',
          state: currentUser?.state || '',
          city: currentUser?.city || '',
        }}
      />

      {quote ? (
        <ConfirmPaymentSheet
          visible={confirmOpen}
          onClose={() => setConfirmOpen(false)}
          onConfirm={payNow}
          quote={quote}
          shippingLabel={feeRowLabel}
          walletAddress={walletAddress}
          tokenBalance={tokenBalance}
          gasBalance={balances.S}
        />
      ) : null}
    </SafeAreaView>
  );
};

/** Review → Confirm → Pay, drawn as a thin segmented bar. */
const StepBar = ({ active }: { active: number }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View style={{ gap: spacing.xs }}>
      <View style={{ flexDirection: 'row', gap: spacing.xs }}>
        {[0, 1, 2].map((i) => (
          <View
            key={i}
            style={{
              flex: 1,
              height: 3,
              borderRadius: radius.pill,
              backgroundColor: i <= active ? colors.accent : colors.border,
            }}
          />
        ))}
      </View>
      <View style={{ flexDirection: 'row' }}>
        {['Review', 'Confirm', 'Pay'].map((label, i) => (
          <Text
            key={label}
            variant="caption"
            weight={i === active ? '600' : '500'}
            color={i === active ? 'accent' : 'textMuted'}
            style={{ flex: 1, textAlign: i === 0 ? 'left' : i === 1 ? 'center' : 'right' }}
          >
            {label}
          </Text>
        ))}
      </View>
    </View>
  );
};

const Notice = ({
  tone,
  icon,
  children,
}: {
  tone: 'success' | 'danger';
  icon: 'star' | 'info';
  children: string;
}) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.xs,
        marginTop: spacing.md,
        paddingVertical: spacing.sm,
        paddingHorizontal: spacing.md,
        backgroundColor: tone === 'success' ? colors.successBg : colors.surfaceAlt,
        borderRadius: radius.sm,
      }}
    >
      <Icon name={icon} size={14} color={tone === 'success' ? colors.success : colors.danger} />
      <Text variant="caption" weight="500" color={tone} style={{ flex: 1 }}>
        {children}
      </Text>
    </View>
  );
};

/** Post-payment receipt: what was paid, where it sits, what happens next. */
const Receipt = ({
  quote,
  orderCount,
  shippingLabel,
  cashbackPercent,
}: {
  quote: CartQuote;
  orderCount: number;
  shippingLabel: string;
  cashbackPercent?: number;
}) => {
  const { colors, spacing, radius } = useTheme();
  const fmt = useQuoteFormat(quote);
  const approx = fmt.approx(quote.totals.total);
  return (
    <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'], gap: spacing.md }}>
      <View style={{ alignItems: 'center', gap: spacing.xs, paddingVertical: spacing.lg }}>
        <View
          style={{
            width: 64,
            height: 64,
            borderRadius: radius.pill,
            backgroundColor: colors.successBg,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.sm,
          }}
        >
          <Icon name="check" size={34} color={colors.success} />
        </View>
        <Text variant="title2" weight="700" align="center" numeric>
          Paid {fmt.token(quote.totals.total)}
        </Text>
        <Text variant="bodySm" color="textSecondary" align="center">
          {[approx, `${orderCount} order${orderCount !== 1 ? 's' : ''} held in escrow`].filter(Boolean).join(' · ')}
        </Text>
      </View>

      <Card>
        <QuoteBreakdown quote={quote} shippingLabel={shippingLabel} withTotal />
      </Card>

      {cashbackPercent ? (
        <Text variant="caption" color="success" align="center">
          {`${cashbackPercent}% cashback lands when you confirm delivery`}
        </Text>
      ) : null}

      <Card style={{ gap: spacing.sm }}>
        <Text variant="overline" color="textMuted">
          What happens next
        </Text>
        <Text variant="bodySm">1. The seller ships, and you get a notification.</Text>
        <Text variant="bodySm">2. You confirm delivery, and the seller is paid from escrow.</Text>
        <Text variant="bodySm">3. If something is wrong, open a dispute from the order.</Text>
      </Card>

      <View style={{ gap: spacing.sm, marginTop: spacing.sm }}>
        <Button title="View Orders" leftIcon="list" onPress={() => router.push('/(dashboard)/orderlist')} />
        <Button title="Continue Shopping" variant="secondary" onPress={() => router.replace('/home')} />
      </View>
    </ScrollView>
  );
};

export default CheckoutScreen;
