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
import React, { useEffect, useMemo, useState, useCallback } from 'react';
import { View, ScrollView, Alert, Image, Platform, KeyboardAvoidingView } from 'react-native';
import { router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '@/AuthContext/AuthContext';
import { cartService, CartSummaryResponse, CartItem, Product } from '@/components/services/cart';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import {
  createOrders,
  prepareApprove,
  prepareEscrow,
  submitEscrow,
  Order,
  type CartQuote,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import { decodeServiceNotes } from '@/components/services/serviceCart';
import LocationPicker from '@/components/common/LocationPicker';
import { getActiveWallet } from '@/services/wallet';
import { useKycVerification } from '@/hooks/useKycVerification';
import { listingAmount, quoteUnits, useCheckoutQuote, useQuoteFormat } from '@/hooks/useCheckoutQuote';
import { useCheckoutBalances } from '@/hooks/useCheckoutBalances';
import { ConfirmPaymentSheet } from '@/components/checkout/ConfirmPaymentSheet';
import { PaymentProgress, type PaymentPhase } from '@/components/checkout/PaymentProgress';
import { QuoteBreakdown } from '@/components/checkout/QuoteBreakdown';
import { useTheme } from '@/theme';
import { MARKETPLACE_TAB } from '@/constants/routes';
import {
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
  const [editingAddress, setEditingAddress] = useState(false);
  const [shippingLocation, setShippingLocation] = useState({ country: '', state: '', city: '' });
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
  const hasAddress = !!shippingLocation.country && !!shippingLocation.state;

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
    city: shippingLocation.city,
    state: shippingLocation.state,
    country: shippingLocation.country,
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

  // Service line items charge a "Call-out fee", not "Shipping". Relabel the
  // aggregated fee row accordingly (mixed carts read "Shipping & call-out").
  const hasServiceItem = items.some((it) => decodeServiceNotes(it.notes) != null);
  const hasProductItem = items.some((it) => decodeServiceNotes(it.notes) == null);
  const feeRowLabel = hasServiceItem ? (hasProductItem ? 'Shipping & call-out' : 'Call-out fee') : 'Shipping';

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
    if (!hasAddress) {
      Alert.alert('Shipping Required', 'Please set your shipping address before checking out.');
      setEditingAddress(true);
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
    if (!quote) return;
    setConfirmOpen(false);
    setPaidQuote(quote);
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
        serviceTiers,
        undefined,
        undefined,
        quote.totals.total,
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
        const submitResult = await submitEscrow(order.id, approveTxHash, escrowTxHash);
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
    }
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
          onBackToCheckout={() => {
            setStep('review');
            setSigningError(null);
            setSigningPhase('idle');
            setOrders([]);
            setCompletedOrders([]);
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

      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 0 : 20}
      >
        <ScrollView
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'], gap: spacing.md }}
          keyboardShouldPersistTaps="handled"
        >
          <StepBar active={0} />

          {/* Shipping Address */}
          <Card>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="nearMe" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
                Deliver to
              </Text>
              <PressableScale onPress={() => setEditingAddress(!editingAddress)} hitSlop={8}>
                <Text variant="bodySm" weight="600" color="accent">
                  {editingAddress ? 'Done' : hasAddress ? 'Change' : 'Add'}
                </Text>
              </PressableScale>
            </View>
            {editingAddress ? (
              <LocationPicker value={shippingLocation} onChange={setShippingLocation} />
            ) : shippingLocation.country ? (
              <View>
                <Text variant="bodyMed" weight="600">
                  {currentUser?.name || currentUser?.username || ''}
                </Text>
                <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                  {[shippingLocation.city, shippingLocation.state, shippingLocation.country].filter(Boolean).join(', ')}
                </Text>
              </View>
            ) : (
              <PressableScale
                onPress={() => setEditingAddress(true)}
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, paddingVertical: spacing.sm }}
              >
                <Icon name="plus" size={18} color={colors.accent} />
                <Text variant="bodySm" weight="600" color="accent">
                  Add shipping address
                </Text>
              </PressableScale>
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
      </KeyboardAvoidingView>

      {/* Fixed Footer */}
      <View
        style={{
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: Platform.OS === 'ios' ? spacing['3xl'] : spacing.lg,
          backgroundColor: colors.surface,
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
          gap: spacing.xs,
        }}
      >
        {quote ? (
          <Text variant="caption" color="textMuted" align="center" numeric>
            {[fmt.token(quote.totals.total), fmt.approx(quote.totals.total)].filter(Boolean).join(' · ')}
          </Text>
        ) : null}
        <Button title="Review payment" leftIcon="shield" disabled={payDisabled} onPress={openConfirm} />
      </View>

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
