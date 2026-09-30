// app/(detail)/checkout/index.tsx
//
// Checkout — review the cart, pick a delivery address and a "Pay with" asset,
// confirm the exact amount in a sheet, then run the escrow flow (createOrders
// → approve → escrow → submit), signing and broadcasting client-side.
//
// FINANCIAL screen. Every amount shown comes from the backend cart quote
// (POST /orders/quote), which is priced by the same code that creates the
// orders — so the total a buyer confirms is the total the wallet is asked to
// approve. The escrow sign/broadcast/submit sequence itself is unchanged.
//
// ONE "Pay with" selector (owner decision 2026-09-29, replacing separate
// chips + a "pay with another token" link) covers three ways to pay
// (utils/checkoutPaySelection.PaySelection):
//   - direct: Sonic USDC or USDT she already holds — today's escrow
//     approve+createOrder, unchanged.
//   - mcgp: Spendable MCGP (the entitlement balance), never wallet MCGP — the
//     backend holds it with no signature and no gas, so the buyer authorizes
//     it with the app's PIN/biometric prompt instead.
//   - swap: any other EVM holding, swapped through LiFi into Sonic USDC in HER
//     OWN wallet first, then paid the same way (services/checkoutFundingRun).
//     That run is app-wide and persisted, so it survives leaving the screen or
//     a restart.
// checkoutPaySelection.defaultPaySelection picks a sensible default (USDC →
// Spendable MCGP → smallest sufficient swap holding → USDC-with-shortfall) and
// is re-run whenever the total changes, but only until she picks for herself.
//
// Everything here is Sonic MAINNET whatever the app's testnet toggle says: the
// escrow lives there, so balances, holdings, signing and the funding swap are
// all pinned to it.
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
import { payOrder, OrderNotPayableError, type EscrowProgress } from '@/services/serviceCheckout';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { decodeServiceNotes } from '@/components/services/serviceCart';
import { getActiveWallet } from '@/services/wallet';
import { useKycVerification } from '@/hooks/useKycVerification';
import { listingAmount, quoteUnits, useCheckoutQuote, useQuoteFormat } from '@/hooks/useCheckoutQuote';
import { useCheckoutBalances } from '@/hooks/useCheckoutBalances';
import { useWalletHoldings } from '@/hooks/useWalletHoldings';
import { ConfirmPaymentSheet } from '@/components/checkout/ConfirmPaymentSheet';
import { DeliveryAddressSheet } from '@/components/checkout/DeliveryAddressSheet';
import { useDeliveryAddresses } from '@/hooks/useDeliveryAddresses';
import { formatDeliveryAddress } from '@/services/deliveryAddressApi';
import { PaymentProgress, type PaymentPhase } from '@/components/checkout/PaymentProgress';
import { QuoteBreakdown } from '@/components/checkout/QuoteBreakdown';
import { useTheme } from '@/theme';
import { MARKETPLACE_TAB } from '@/constants/routes';
import AssetPicker, { type AssetPickerExtraOption } from '@/components/assets/AssetPicker';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import { getChainByChainId } from '@/constants/chains';
import { isNativeLiFiToken } from '@/services/assetCatalog';
import { FundingConfirmSheet, FundingPriceLine, FundingProgressView } from '@/components/checkout/PayWithToken';
import { useCheckoutFundingRun, useFundingPrice, type FundingPick } from '@/hooks/useCheckoutFunding';
import { canForgetRun } from '@/services/checkoutFundingRun';
import { fundingPickerAllow } from '@/utils/fundingPicker';
import {
  defaultPaySelection,
  orderTokenFor,
  payShortfallMessage,
  swapCandidatesFromHoldings,
  type PaySelection,
} from '@/utils/checkoutPaySelection';
import { useNetwork } from '@/hooks/useNetwork';
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

// The "Swap to pay" side of the selector always targets Sonic USDC (see
// checkoutPaySelection.orderTokenFor): a held Sonic USDT still pays directly,
// never as a swap into itself.
const SWAP_ALLOW = fundingPickerAllow('USDC');

const formatBalance = (n: number) => n.toLocaleString('en-US', { maximumFractionDigits: 2 });

const CheckoutScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { currentUser, token } = useAuth();
  const { requireKycVerified } = useKycVerification();
  const [cartData, setCartData] = useState<CartSummaryResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [step, setStep] = useState<CheckoutStep>('review');
  // What she is about to pay with (checkoutPaySelection.PaySelection): direct
  // Sonic USDC/USDT, Spendable MCGP, or a swap. Starts null (still computing
  // the default) and is re-picked whenever the total changes, but only until
  // `manualSelection` is set — she chose for herself via the "Change" picker.
  const [selection, setSelection] = useState<PaySelection | null>(null);
  const manualSelection = useRef(false);
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
  // What runs once the buyer authorizes a Spendable MCGP payment (a first
  // attempt or a retry); null while no prompt is open.
  const [authorizeAction, setAuthorizeAction] = useState<(() => void) | null>(null);
  const {
    addresses,
    selected: deliveryAddress,
    select: selectAddress,
    loading: addressesLoading,
    error: addressesError,
    reload: reloadAddresses,
  } = useDeliveryAddresses();
  const { currency, rate } = useCurrency();
  const { network } = useNetwork();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [fundingConfirmOpen, setFundingConfirmOpen] = useState(false);
  const { state: funding, runner: fundingRunner } = useCheckoutFundingRun();

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
  const isMCGP = selection?.kind === 'mcgp';
  const swapMode = selection?.kind === 'swap';
  // The order token an order/quote is created for — always USDC for a swap
  // (checkoutPaySelection.orderTokenFor).
  const orderToken = orderTokenFor(selection);
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

  // Always-on quotes for the two tokens the default-selection algorithm needs
  // to compare against her balances (checkoutPaySelection.defaultPaySelection)
  // — a direct USDC payment and Spendable MCGP both need the total in their
  // own units. A USDT quote runs only when she has actually picked Sonic USDT
  // directly (not part of the default, only a manual "Pay directly" choice).
  const quoteBase = { city: location?.city ?? '', state: location?.state ?? '', country: location?.country ?? '', serviceTiers, cartKey };
  const reviewing = hasAddress && items.length > 0 && step === 'review';
  const usdcQuote = useCheckoutQuote({ token: 'USDC', ...quoteBase, enabled: reviewing });
  const mcgpQuote = useCheckoutQuote({ token: 'MCGP', ...quoteBase, enabled: reviewing });
  const usdtQuote = useCheckoutQuote({
    token: 'USDT', ...quoteBase,
    enabled: reviewing && selection?.kind === 'direct' && selection.token === 'USDT',
  });
  const activeQuote = isMCGP ? mcgpQuote : orderToken === 'USDT' ? usdtQuote : usdcQuote;
  const { quote, loading: quoteLoading, error: quoteError, refresh: refreshQuote } = activeQuote;
  const fmt = useQuoteFormat(quote);
  const paidFmt = useQuoteFormat(paidQuote);
  const { balances, address: walletAddress, refresh: refreshBalances } = useCheckoutBalances();
  // What "Swap to pay" may offer, and what the smart default picks from — held
  // regardless of the app's testnet toggle: a checkout swap is always Sonic
  // mainnet (see useWalletHoldings).
  const { holdings } = useWalletHoldings(true, 'mainnet');

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

  // A run this phone left (a kill mid-bridge, or she left the screen) is picked
  // up on opening checkout: it signs nothing new, only follows what was sent.
  useEffect(() => {
    void fundingRunner.resume();
  }, [fundingRunner]);

  // The smart default (checkoutPaySelection spec §3): USDC → Spendable MCGP →
  // smallest sufficient swap holding → USDC-with-shortfall — re-run whenever
  // the total or a balance changes, but only until she picks for herself.
  useEffect(() => {
    if (manualSelection.current || !reviewing) return;
    const usdcNeeded = usdcQuote.quote ? quoteUnits(usdcQuote.quote.totals.total, usdcQuote.quote.decimals) : null;
    const mcgpNeeded = mcgpQuote.quote ? quoteUnits(mcgpQuote.quote.totals.total, mcgpQuote.quote.decimals) : null;
    const totalUsd = usdcQuote.quote && usdcNeeded != null ? usdcNeeded * usdcQuote.quote.usdPerToken : null;
    setSelection(defaultPaySelection({
      usdcBalance: balances.USDC,
      usdcNeeded,
      mcgpBalance: balances.MCGP,
      mcgpNeeded,
      totalUsd,
      swapCandidates: swapCandidatesFromHoldings(holdings),
    }));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- usdcQuote/mcgpQuote objects are the real trigger; their .quote identity changes when the total does.
  }, [usdcQuote.quote, mcgpQuote.quote, balances.USDC, balances.MCGP, holdings, reviewing]);

  // A swap always targets Sonic USDC in her own wallet (checkoutFundingRun);
  // derived from `selection` rather than kept as its own state, so the two can
  // never drift apart.
  const fundingPick: FundingPick | null = useMemo(
    () => (selection?.kind === 'swap' ? { token: selection.token, chainId: selection.chainId } : null),
    [selection],
  );
  // The cart quote's own fields, so the server prices the very cart the orders
  // will be created from (useFundingPrice keys on its content, not identity).
  const fundingCart = location
    ? {
        token: orderToken,
        buyerCity: location.city,
        buyerState: location.state,
        buyerCountry: location.country,
        serviceTiers,
        deliveryAddressId: deliveryAddress?.id,
      }
    : null;
  const {
    price: fundingPrice,
    pricing: fundingPricing,
    stale: fundingStale,
    reprice: repriceFunding,
  } = useFundingPrice({
    pick: fundingPick,
    wallet: walletAddress,
    cart: fundingCart,
    live: swapMode && step === 'review' && funding.stage === 'idle',
  });
  // "Pay again" for a checkout whose orders exist but whose swap was never
  // sent: priced from the orders themselves, with the token she picked then.
  const retryRecord =
    funding.stage === 'failed' && funding.outcome === 'retry' && funding.record && !funding.record.intentId && !funding.record.swapHash
      ? funding.record
      : null;
  const retryPick = useMemo(
    () => (retryRecord ? { token: retryRecord.token, chainId: retryRecord.chainId } : null),
    // Same run, same pick: only a different group re-prices.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [retryRecord?.groupId],
  );
  const { price: retryPrice, stale: retryStale, reprice: repriceRetry } = useFundingPrice({
    pick: retryPick,
    wallet: retryRecord?.wallet ?? null,
    groupId: retryRecord?.groupId ?? null,
    live: !!retryRecord,
  });
  const [retryConfirmOpen, setRetryConfirmOpen] = useState(false);

  const tokenBalance =
    selection?.kind === 'mcgp' ? balances.MCGP : selection?.kind === 'direct' ? balances[selection.token] : null;
  const totalUnits = quote ? quoteUnits(quote.totals.total, quote.decimals) : null;
  const shortOnToken = !swapMode && tokenBalance != null && totalUnits != null && tokenBalance < totalUnits;
  const shortfallText =
    shortOnToken && quote && selection && (selection.kind === 'direct' || selection.kind === 'mcgp')
      ? payShortfallMessage({
          kind: selection.kind,
          token: selection.kind === 'direct' ? selection.token : undefined,
          neededLabel: fmt.token(quote.totals.total),
          haveLabel: `${formatBalance(tokenBalance!)} ${selection.kind === 'mcgp' ? 'MCGP' : selection.token}`,
        })
      : null;

  // The row under "Pay with": what to draw and what it says, for whichever
  // kind of selection is active.
  const payRow = useMemo(() => {
    if (!selection) return null;
    if (selection.kind === 'direct') {
      return {
        symbol: selection.token, name: selection.token, chainLabel: 'Sonic', chainKey: 'sonic' as const,
        balance: balances[selection.token] ?? 0, badge: undefined as string | undefined,
      };
    }
    if (selection.kind === 'mcgp') {
      return {
        symbol: 'MCGP', name: 'Spendable MCGP', chainLabel: 'No gas, no platform fee', chainKey: 'sonic' as const,
        balance: balances.MCGP ?? 0, badge: 'TSA',
      };
    }
    const held = holdings.find(h => h.chainId === selection.chainId
      && (h.native ? isNativeLiFiToken(selection.token) : h.address.toLowerCase() === selection.token.address.toLowerCase()));
    return {
      symbol: selection.token.symbol, name: selection.token.symbol,
      chainLabel: getChainByChainId(selection.chainId)?.name ?? `Chain ${selection.chainId}`,
      chainKey: getChainByChainId(selection.chainId)?.key,
      balance: held?.balance ?? 0, badge: undefined as string | undefined,
    };
  }, [selection, balances, holdings]);

  // "Pay directly" — always Sonic USDC (even unfunded, the default target)
  // and Spendable MCGP; Sonic USDT joins only when she actually holds some.
  const payDirectOptions: AssetPickerExtraOption[] = useMemo(() => {
    const pick = (sel: PaySelection) => { manualSelection.current = true; setSelection(sel); };
    const options: AssetPickerExtraOption[] = [
      {
        key: 'usdc', symbol: 'USDC', chainLabel: 'Sonic', chainKey: 'sonic',
        balance: balances.USDC ?? 0, onSelect: () => pick({ kind: 'direct', token: 'USDC' }),
      },
    ];
    if ((balances.USDT ?? 0) > 0) {
      options.push({
        key: 'usdt', symbol: 'USDT', chainLabel: 'Sonic', chainKey: 'sonic',
        balance: balances.USDT ?? 0, onSelect: () => pick({ kind: 'direct', token: 'USDT' }),
      });
    }
    options.push({
      key: 'mcgp', symbol: 'MCGP', label: 'Spendable MCGP', chainLabel: 'No gas, no platform fee', chainKey: 'sonic',
      badge: 'TSA', balance: balances.MCGP ?? 0, onSelect: () => pick({ kind: 'mcgp' }),
    });
    return options;
  }, [balances.USDC, balances.USDT, balances.MCGP]);

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
    if (swapMode) {
      if (!fundingPick) {
        setPickerOpen(true);
        return;
      }
      if (!fundingPrice?.quote) return;
      if (fundingStale) void repriceFunding();
      setFundingConfirmOpen(true);
      return;
    }
    refreshBalances();
    setConfirmOpen(true);
  };

  // --- Pay with another token: swap into the order token, then escrow each order ---
  const payWithFunding = () => {
    const priced = fundingPrice?.quote;
    if (!fundingPick || !priced || !quote || !location || !walletAddress) return;
    // A stale figure is not paid: look it up, show what it is now, and let her confirm that.
    if (fundingStale) {
      void repriceFunding();
      return;
    }
    setFundingConfirmOpen(false);
    setPaidQuote(quote);
    void fundingRunner.pay({
      token: fundingPick.token,
      chainId: fundingPick.chainId,
      wallet: walletAddress,
      pricedFromAmount: priced.fromAmount,
      createOrders: async () => {
        const res = await createOrders(
          orderToken,
          location.city,
          location.state,
          location.country,
          serviceTiers,
          undefined,
          undefined,
          quote.totals.total,
          deliveryAddress?.id,
          true,
        );
        if (res.code === 'price_changed') {
          setRepriced(true);
          throw new Error('The total changed since you confirmed it. Review the new amount before paying.');
        }
        if (!res.success || !res.data?.orders?.length || !res.data.checkoutGroupId) {
          throw new Error(res.message || 'Failed to create orders');
        }
        cartService.notifyChange();
        return { orders: res.data.orders, groupId: res.data.checkoutGroupId };
      },
    });
  };

  const payAgain = () => {
    if (!retryPrice?.quote) {
      void repriceRetry();
      return;
    }
    setRetryConfirmOpen(true);
  };

  const confirmPayAgain = () => {
    if (!retryPrice?.quote) return;
    if (retryStale) {
      void repriceRetry();
      return;
    }
    setRetryConfirmOpen(false);
    void fundingRunner.retry(retryPrice.quote.fromAmount);
  };

  // --- Step 2 → 3 ---
  // Spendable MCGP moves without a wallet signature, so the app's own
  // authorization prompt stands in for it (as for bills and transfers).
  const confirmPayment = () => {
    if (!isMCGP) {
      payNow();
      return;
    }
    setConfirmOpen(false);
    setAuthorizeAction(() => payNow);
  };

  // --- the checkout flow: create orders, then pay each ---
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
        orderToken,
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
  // The same list, readable inside a payment run that started before it grew.
  const unpayableRef = useRef<string[]>([]);
  unpayableRef.current = unpayable;
  // One payment run at a time: a double tap on "Try again" must not sign twice.
  const paying = useRef(false);

  // Pays each order not skipped — escrow for stablecoins, a Spendable MCGP
  // hold for MCGP (payOrder). The run succeeds only when every order ends up paid.
  const payOrders = async (list: Order[], skip: string[]) => {
    if (paying.current) return;
    paying.current = true;
    try {
      for (let i = 0; i < list.length; i++) {
        const order = list[i];
        if (skip.includes(order.id)) continue;
        setCurrentOrderIndex(i);
        try {
          await payOrder(
            order,
            (phase) => {
              if (
                phase === 'approving_token' ||
                phase === 'creating_escrow' ||
                phase === 'submitting' ||
                phase === 'paying_spendable'
              ) {
                setSigningPhase(phase);
              }
            },
            (orderProgress.current[order.id] ??= {}),
          );
        } catch (e) {
          if (e instanceof OrderNotPayableError) {
            unpayableRef.current = [...unpayableRef.current, order.id];
            setUnpayable(unpayableRef.current);
          }
          throw e;
        }
        setCompletedOrders((prev) => [...prev, order.id]);
      }
      if (unpayableRef.current.length > 0) {
        throw new Error('Some orders could no longer be paid — they were cancelled. Nothing was charged for them.');
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
    const resume = () => {
      setSigningError(null);
      payOrders(orders, [...completedOrders, ...unpayable]);
    };
    // A retry moves Spendable MCGP too, so it is authorized again.
    if (paidQuote?.token === 'MCGP') setAuthorizeAction(() => resume);
    else resume();
  };

  const authQuote = paidQuote ?? quote;
  const authorizeGate = (
    <AuthorizationModal
      visible={authorizeAction != null}
      title="Authorize payment"
      description={
        authQuote
          ? `Pay ${(paidQuote ? paidFmt : fmt).token(authQuote.totals.total)} from your Spendable MCGP. It is held until you confirm delivery.`
          : undefined
      }
      onAuthorized={() => {
        const run = authorizeAction;
        setAuthorizeAction(null);
        run?.();
      }}
      onCancel={() => setAuthorizeAction(null)}
    />
  );

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

  // ===== PAYING WITH ANOTHER TOKEN (a run in progress, or how it ended) =====
  if (funding.stage !== 'idle') {
    const rec = funding.record;
    const orderNames = (rec?.orders ?? []).map((o, i) => {
      const line = paidQuote?.lines.find((l) => l.productId === o.productId);
      return line?.name ?? `order #${o.id.slice(0, 8)}`;
    });
    // Forget the run only when nothing is in flight (done, ended, or nothing
    // sent); otherwise just go — the record and hash stay, and checkout
    // follows the run again when she comes back.
    const leave = (to: () => void) => {
      if (canForgetRun(funding)) void fundingRunner.discard().finally(to);
      else to();
    };
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header title="Paying" showBack={funding.stage === 'failed' || funding.stage === 'done' || funding.stage === 'bridging'} />
        <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
          <FundingProgressView
            state={funding}
            sourceSymbol={rec?.token.symbol ?? fundingPick?.token.symbol ?? 'your token'}
            orderToken={rec?.orders[0]?.token ?? orderToken}
            orderNames={orderNames}
            onContinue={() => void fundingRunner.resume()}
            onPayAgain={payAgain}
            onViewOrders={() => leave(() => router.replace('/(dashboard)/orderlist'))}
            onDone={() => leave(() => {
              setPaidQuote(null);
              setStep('review');
              loadCart();
            })}
          />
        </ScrollView>
        {retryRecord && retryPrice?.quote ? (
          <FundingConfirmSheet
            visible={retryConfirmOpen}
            onClose={() => setRetryConfirmOpen(false)}
            onConfirm={confirmPayAgain}
            quote={retryPrice.quote}
            pick={{ token: retryRecord.token, chainId: retryRecord.chainId }}
            stale={retryStale}
          />
        ) : null}
      </SafeAreaView>
    );
  }

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
        {authorizeGate}
      </SafeAreaView>
    );
  }

  // ===== REVIEW (MAIN CHECKOUT) =====
  const payDisabled = swapMode
    ? !quote || quoteLoading || !fundingPick || !fundingPrice?.quote
    : !quote || quoteLoading || shortOnToken;
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

        {/* Pay with — one selector: direct Sonic USDC/USDT, Spendable MCGP, or a swap. */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
            <Icon name="wallet" size={16} color={colors.accent} />
            <Text variant="bodyMed" weight="600">
              Pay with
            </Text>
          </View>
          {network === 'testnet' ? (
            <Notice tone="danger" icon="info">
              {'Marketplace payments use real funds on Sonic mainnet.'}
            </Notice>
          ) : null}
          {payRow ? (
            <PressableScale
              onPress={() => setPickerOpen(true)}
              accessibilityRole="button"
              accessibilityLabel={`Pay with ${payRow.name}, ${payRow.chainLabel}, balance ${formatBalance(payRow.balance)}. Change`}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.md,
                paddingVertical: spacing.sm,
                marginTop: network === 'testnet' ? spacing.sm : 0,
              }}
            >
              <TokenAvatar symbol={payRow.symbol} chainKey={payRow.chainKey} size={40} />
              <View style={{ flex: 1 }}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <Text variant="bodyMed" weight="700">{payRow.name}</Text>
                  {payRow.badge ? (
                    <View style={{ backgroundColor: colors.accentMuted, borderRadius: radius.pill, paddingHorizontal: 6, paddingVertical: 1 }}>
                      <Text variant="caption" weight="700" color="accent">{payRow.badge}</Text>
                    </View>
                  ) : null}
                </View>
                <Text variant="bodySm" color="textSecondary" numeric>
                  {`${payRow.chainLabel} · ${formatBalance(payRow.balance)}`}
                </Text>
              </View>
              <Text variant="bodySm" weight="600" color="accent">Change</Text>
              <Icon name="chevronRight" size={18} color={colors.textMuted} />
            </PressableScale>
          ) : (
            <Spinner label="Loading your balances…" />
          )}
          {swapMode ? (
            <FundingPriceLine pick={fundingPick} quote={fundingPrice?.quote} pricing={fundingPricing} error={fundingPrice?.error} />
          ) : null}
          {swapMode ? null : shortfallText ? (
            <Notice tone="danger" icon="info">{shortfallText}</Notice>
          ) : isMCGP ? (
            <Notice tone="success" icon="star">
              Paid from your Spendable MCGP: no gas, no platform fee
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

      <AssetPicker
        visible={pickerOpen}
        side="from"
        holdingsOnly
        holdingsNetwork="mainnet"
        title="Pay with"
        allow={SWAP_ALLOW}
        note="Coins on other networks can't pay here; MCGP in your wallet can't pay."
        extraSections={[{ id: 'direct', title: 'Pay directly', options: payDirectOptions }]}
        sectionTitleOverrides={{ tsa: 'Swap to pay', major: 'Swap to pay', wallet: 'Swap to pay' }}
        onSelect={(token, chainId) => {
          manualSelection.current = true;
          setSelection({ kind: 'swap', token: { ...token, chainId }, chainId });
          setPickerOpen(false);
        }}
        onClose={() => setPickerOpen(false)}
      />
      {swapMode && fundingPick && fundingPrice?.quote ? (
        <FundingConfirmSheet
          visible={fundingConfirmOpen}
          onClose={() => setFundingConfirmOpen(false)}
          onConfirm={payWithFunding}
          quote={fundingPrice.quote}
          pick={fundingPick}
          stale={fundingStale}
        />
      ) : null}
      {quote ? (
        <ConfirmPaymentSheet
          visible={confirmOpen}
          onClose={() => setConfirmOpen(false)}
          onConfirm={confirmPayment}
          quote={quote}
          shippingLabel={feeRowLabel}
          walletAddress={walletAddress}
          tokenBalance={tokenBalance}
          gasBalance={balances.S}
        />
      ) : null}
      {authorizeGate}
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
