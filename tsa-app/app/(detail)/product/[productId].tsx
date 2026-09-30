// app/(detail)/product/[productId].tsx
//
// Product detail — photo pager, info, the buyer's choices (options, quantity,
// note), seller, description, delivery, and a safe-area-aware action bar.
//
// Checkout pays for the whole cart, so Buy now puts the item in the cart first.
// It sends `replace` so the line becomes exactly this pick: repeat taps never
// grow the cart. Because replace overwrites the line, the choices open
// prefilled from it — Buy now never silently drops a colour or note the buyer
// added earlier. The in-cart strip shows what the cart already holds.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  View,
  ScrollView,
  Image,
  TouchableOpacity,
  Alert,
  NativeScrollEvent,
  NativeSyntheticEvent,
  useWindowDimensions,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack, useFocusEffect } from 'expo-router';
import { goBack } from '@/utils/back';
import { MARKETPLACE_TAB } from '@/constants/routes';
import api, { Product } from '@/components/services/api';
import { cartService, AddToCartRequest, CartItem } from '@/components/services/cart';
import { isServiceNote } from '@/components/services/serviceCart';
import { useCartCount } from '@/contexts/CartContext';
import { useAuth } from '@/AuthContext/AuthContext';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import { cartLineProductId, quantityInCart } from '@/utils/cartLine';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Avatar,
  BottomActionBar,
  Button,
  Card,
  Chip,
  EmptyState,
  Icon,
  type IconName,
  Input,
  PressableScale,
  Spinner,
  Text,
  useToast,
} from '@/components/ui';

// parseSpecOptions splits a spec value ("White pink black blue" or "S, M, L")
// into individual, selectable options. Prefers comma separation, falls back to
// whitespace; trims, drops blanks, de-dupes while preserving order.
function parseSpecOptions(value: string): string[] {
  if (!value) return [];
  const raw = value.includes(',') ? value.split(',') : value.split(/\s+/);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const t of raw) {
    const v = t.trim();
    if (v && !seen.has(v.toLowerCase())) {
      seen.add(v.toLowerCase());
      out.push(v);
    }
  }
  return out;
}

const IMAGE_ASPECT = 0.85;
// Past this many characters the description opens clamped behind "Read more".
const DESCRIPTION_CLAMP_CHARS = 220;
const DESCRIPTION_CLAMP_LINES = 4;
// Stock at or below this reads as scarcity ("2 left") rather than a count.
const LOW_STOCK = 5;

type CartAction = 'add' | 'buy';

export default function ProductDetailsScreen() {
  const { productId, productData: productDataParam } = useLocalSearchParams<{
    productId: string;
    productData?: string;
  }>();
  const router = useRouter();
  const toast = useToast();
  const { colors, spacing, radius } = useTheme();

  const [product, setProduct] = useState<Product | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { token } = useAuth();
  const { cartCount } = useCartCount();
  const { width: windowWidth } = useWindowDimensions();
  const imageHeight = windowWidth * IMAGE_ASPECT;
  const [quantity, setQuantity] = useState(1);
  const [selectedImageIndex, setSelectedImageIndex] = useState(0);
  const [inCartQty, setInCartQty] = useState(0);
  const [pendingAction, setPendingAction] = useState<CartAction | null>(null);
  const [descriptionOpen, setDescriptionOpen] = useState(false);
  const [noteOpen, setNoteOpen] = useState(false);
  // Buyer-selected specs (attribute name → chosen option values) + a free-text
  // purchase note, so the merchant knows how to arrange the order.
  const [selectedSpecs, setSelectedSpecs] = useState<Record<string, string[]>>({});
  const [purchaseNotes, setPurchaseNotes] = useState('');

  // A tap lands before React re-renders the disabled button, so a ref (not the
  // pendingAction state) is what keeps a double tap from sending twice.
  const inFlight = useRef(false);
  // Cart reads can overlap (focus, then an add); only the latest one lands.
  const cartReadSeq = useRef(0);
  // The choices are prefilled from the cart line once per product, and never
  // over something the buyer already changed.
  const prefilledFor = useRef<string | null>(null);
  const picksTouched = useRef(false);

  const changeQuantity = (next: number) => {
    picksTouched.current = true;
    setQuantity(next);
  };
  const changeNotes = (next: string) => {
    picksTouched.current = true;
    setPurchaseNotes(next);
  };

  const toggleSpec = useCallback((name: string, option: string) => {
    picksTouched.current = true;
    setSelectedSpecs((prev) => {
      const cur = prev[name] ?? [];
      const next = cur.includes(option)
        ? cur.filter((v) => v !== option)
        : [...cur, option];
      return { ...prev, [name]: next };
    });
  }, []);

  // Flatten the selection into one {name, value} per attribute (values joined),
  // matching the backend CartItemAttribute shape. Only attributes with a pick.
  const buildSelectedAttributes = useCallback(
    (): { name: string; value: string }[] =>
      Object.entries(selectedSpecs)
        .filter(([, vals]) => vals.length > 0)
        .map(([name, vals]) => ({ name, value: vals.join(', ') })),
    [selectedSpecs],
  );
  const { currency, rate } = useCurrency();

  const formatPrice = (price: number) =>
    formatPriceUtil(price, { currency, rate });

  // Take in the cart's lines: how many of this product it holds, and — the
  // first time only — the buyer's earlier choices for it.
  const applyCartItems = useCallback(
    (items: CartItem[] | undefined) => {
      const id = productId as string;
      setInCartQty(quantityInCart(items, id));
      if (prefilledFor.current === id) return;
      prefilledFor.current = id;
      const line = items?.find((item) => cartLineProductId(item) === id);
      if (!line || picksTouched.current) return;
      setQuantity(Math.max(1, line.quantity));
      const specs: Record<string, string[]> = {};
      for (const attr of line.selectedAttributes ?? []) {
        specs[attr.name] = attr.value.split(', ').filter(Boolean);
      }
      setSelectedSpecs(specs);
      if (line.notes && !isServiceNote(line.notes)) setPurchaseNotes(line.notes);
    },
    [productId],
  );

  // /cart/summary never creates a cart (GET /cart does). It is a heavy read, so
  // it runs when the screen gains focus rather than on every cart change; this
  // screen's own adds update the strip from the add response instead.
  const loadCartLine = useCallback(async () => {
    if (!token) return;
    const seq = ++cartReadSeq.current;
    try {
      const summary = await cartService.getCartSummary();
      if (seq !== cartReadSeq.current) return;
      if (summary.success) applyCartItems(summary.data?.cart?.items);
      else if (summary.message === 'Cart not found') applyCartItems([]);
      // Any other failure keeps the last known count.
    } catch {}
  }, [token, applyCartItems]);

  useEffect(() => {
    // A new product starts from a clean slate.
    setSelectedImageIndex(0);
    setQuantity(1);
    setSelectedSpecs({});
    setPurchaseNotes('');
    setInCartQty(0);
    prefilledFor.current = null;
    picksTouched.current = false;
  }, [productId]);

  useEffect(() => {
    if (token) {
      api.setToken(token);
      cartService.setToken(token);
    }
    loadProduct();
  }, [productId, token]);

  // A prefilled quantity can exceed stock that has since sold down.
  useEffect(() => {
    if (product && product.stock > 0 && quantity > product.stock) setQuantity(product.stock);
  }, [product, quantity]);

  useFocusEffect(
    useCallback(() => {
      loadCartLine();
    }, [loadCartLine]),
  );

  // Services and products share the same backend collection, so a service id
  // can land on this product route from any "View product" link. Detect it
  // here and bounce to /servicedetail so services never render with stock,
  // cart and "Buy Now" UI.
  const redirectIfService = useCallback(
    (p: any): boolean => {
      if (p?.type !== 'Service') return false;
      const heroImage =
        p.images?.find((img: any) => img?.url)?.url ||
        (typeof (p as any).image === 'string' ? (p as any).image : '') ||
        '';
      router.replace({
        pathname: '/servicedetail',
        params: {
          id: p.id || p._id || (productId as string),
          title: p.name ?? '',
          description: p.description ?? '',
          image: heroImage,
        },
      });
      return true;
    },
    [router, productId],
  );

  const loadProduct = async () => {
    if (!productId) return;

    if (productDataParam) {
      try {
        const parsed = JSON.parse(productDataParam as string);
        if (!parsed._id && parsed.id) parsed._id = parsed.id;
        if (redirectIfService(parsed)) return;
        setProduct(parsed);
        setLoading(false);
        return;
      } catch (e) {
        console.warn('Failed to parse productData param, falling back to API');
      }
    }

    try {
      setLoading(true);
      setError(null);
      const response = await api.getProductById(productId as string);
      if (response.success && response.data) {
        if (redirectIfService(response.data)) return;
        setProduct(response.data);
      } else {
        setError(response.message || 'Failed to load product details');
      }
    } catch (error: any) {
      console.error('Fetch product error:', error);
      setError(
        error.message || 'An error occurred while fetching product details',
      );
    } finally {
      setLoading(false);
    }
  };

  // Get valid image URLs from product
  // Handles both API shape (images: [{url, id, ...}]) and marketplace shape (image: string)
  const getValidImages = useCallback(() => {
    const results: Array<{ url: string }> = [];

    // Handle images array (API product shape)
    if (product?.images?.length) {
      for (const img of product.images) {
        if (img.url && img.url.startsWith('http')) {
          results.push({ url: img.url });
        }
      }
    }

    // Handle singular image field (marketplace product shape)
    if (results.length === 0 && (product as any)?.image) {
      const img = (product as any).image;
      if (typeof img === 'string' && img.startsWith('http')) {
        results.push({ url: img });
      }
    }

    return results;
  }, [product]);

  const requireLogin = (message: string): boolean => {
    if (token) return false;
    Alert.alert('Login Required', message, [
      { text: 'Cancel', style: 'cancel' },
      { text: 'Login', onPress: () => router.push('/login') },
    ]);
    return true;
  };

  // One cart write per tap, whichever button: `replace` is Buy now's — the line
  // becomes this pick. Add to cart keeps adding on top, as a cart should.
  const sendToCart = async (action: CartAction): Promise<boolean> => {
    if (!product || inFlight.current) return false;
    inFlight.current = true;
    setPendingAction(action);
    try {
      const selectedAttributes = buildSelectedAttributes();
      const request: AddToCartRequest = {
        productId: product._id || product.id || '',
        quantity,
        ...(selectedAttributes.length > 0 ? { selectedAttributes } : {}),
        ...(purchaseNotes.trim() ? { notes: purchaseNotes.trim() } : {}),
        ...(action === 'buy' ? { replace: true } : {}),
      };
      const res = await cartService.addToCart(request);
      if (!res.success) {
        toast.error(res.message || 'Could not update your cart. Try again.');
        return false;
      }
      // The add returns the whole cart; it supersedes any read still in flight.
      cartReadSeq.current += 1;
      applyCartItems(res.data?.items);
      return true;
    } catch (error: any) {
      toast.error(error.message || 'Could not update your cart. Try again.');
      return false;
    } finally {
      inFlight.current = false;
      setPendingAction(null);
    }
  };

  const handleAddToCart = async () => {
    if (requireLogin('Please login to add items to your cart')) return;
    if (await sendToCart('add')) toast.success(`Added ${quantity} to your cart`);
  };

  const handleBuyNow = async () => {
    if (requireLogin('Please login to continue')) return;
    if (await sendToCart('buy')) router.push('/checkout');
  };

  const handleContactSeller = () => {
    if (!product) return;
    router.push(
      `/seller/${product._id || product.id}?sellerEmail=${encodeURIComponent(product.email)}`,
    );
  };

  const getCategoryName = () => {
    if (!product?.category) return product?.categoryName || null;
    if (typeof product.category === 'string')
      return product.categoryName || null;
    return product.category.title || product.categoryName || null;
  };

  const onGalleryScroll = (e: NativeSyntheticEvent<NativeScrollEvent>) => {
    const { contentOffset, layoutMeasurement } = e.nativeEvent;
    const pageWidth = layoutMeasurement.width || windowWidth;
    const last = Math.max(0, getValidImages().length - 1);
    const index = Math.min(last, Math.max(0, Math.round(contentOffset.x / pageWidth)));
    if (index !== selectedImageIndex) setSelectedImageIndex(index);
  };

  // Header cart button — preserves the cartCount state + /cart navigation.
  const headerCartButton = () => (
    <TouchableOpacity
      onPress={() => router.push('/cart')}
      activeOpacity={0.7}
      accessibilityLabel={`Cart, ${cartCount} items`}
      style={{ marginRight: spacing.sm, padding: spacing.xs, position: 'relative' }}
    >
      <Icon name="cart" size={22} color={colors.accent} />
      {cartCount > 0 && (
        <View
          style={{
            position: 'absolute',
            top: 0,
            right: 0,
            backgroundColor: colors.danger,
            borderRadius: radius.pill,
            minWidth: 18,
            height: 18,
            justifyContent: 'center',
            alignItems: 'center',
            paddingHorizontal: 4,
            borderWidth: 1.5,
            borderColor: colors.surface,
          }}
        >
          <Text variant="overline" weight="700" style={{ color: colors.onAccent }}>
            {cartCount > 99 ? '99+' : cartCount}
          </Text>
        </View>
      )}
    </TouchableOpacity>
  );

  // --- Loading ---
  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Stack.Screen options={{ title: 'Product Details' }} />
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading product…" />
        </View>
      </SafeAreaView>
    );
  }

  // --- Error ---
  if (error || !product) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Stack.Screen options={{ title: 'Product Details' }} />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="cart"
            title="Product Not Found"
            subtitle={
              error ||
              'This product may have been removed or is no longer available.'
            }
            actionLabel="Try Again"
            onAction={loadProduct}
          />
          <View style={{ alignItems: 'center', marginTop: spacing.sm }}>
            <PressableScale onPress={() => goBack(MARKETPLACE_TAB)} hitSlop={8} style={{ paddingVertical: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">
                Go Back
              </Text>
            </PressableScale>
          </View>
        </View>
      </SafeAreaView>
    );
  }

  const validImages = getValidImages();
  const categoryName = getCategoryName();
  const isInStock = product.stock > 0;
  // Nothing more to add once the cart holds every unit in stock.
  const cartHoldsAllStock = inCartQty >= product.stock;
  const ratingCount = product.rating?.count || 0;
  const description = product.description?.trim() ?? '';
  const descriptionClamped =
    description.length > DESCRIPTION_CLAMP_CHARS && !descriptionOpen;
  const showNote = noteOpen || purchaseNotes.length > 0;

  // The four zones checkout charges (GetShippingRate). A zone priced at 0 is
  // free; a fee the listing doesn't carry is left out rather than guessed.
  const deliveryRows: { icon: IconName; label: string; fee?: number }[] = [
    { icon: 'home', label: 'Same city', fee: product.shippingSameCity },
    { icon: 'nearMe', label: 'Same state', fee: product.shippingSameState },
    { icon: 'send', label: 'Nationwide', fee: product.shippingSameCountry },
    { icon: 'send', label: 'International', fee: product.shippingInternational },
  ];

  const section = { marginTop: spacing.sm, borderRadius: 0 } as const;
  const stepButton = (enabled: boolean) => ({
    width: 36,
    height: 36,
    justifyContent: 'center' as const,
    alignItems: 'center' as const,
    opacity: enabled ? 1 : 0.4,
  });

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* The title was blank while the drawer header sat above this one; now
          that this is the screen's only header, it has to name the screen. */}
      <Stack.Screen
        options={{
          title: product.name || 'Product Details',
          headerRight: headerCartButton,
        }}
      />

      <ScrollView
        style={{ flex: 1 }}
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ paddingBottom: spacing.lg }}
      >
        {/* ===== Photos ===== */}
        <View style={{ backgroundColor: colors.surface, width: '100%', height: imageHeight }}>
          {validImages.length > 0 ? (
            <>
              <ScrollView
                horizontal
                pagingEnabled
                showsHorizontalScrollIndicator={false}
                onMomentumScrollEnd={onGalleryScroll}
              >
                {validImages.map((img, i) => (
                  <Image
                    key={i}
                    source={{ uri: img.url }}
                    style={{ width: windowWidth, height: imageHeight }}
                    accessible
                    resizeMode="cover"
                    accessibilityLabel={`${product.name}, photo ${i + 1} of ${validImages.length}`}
                  />
                ))}
              </ScrollView>
              {validImages.length > 1 && (
                <>
                  <View
                    style={{
                      position: 'absolute',
                      top: spacing.md,
                      right: spacing.md,
                      backgroundColor: colors.overlay,
                      borderRadius: radius.pill,
                      paddingHorizontal: spacing.sm,
                      paddingVertical: 2,
                    }}
                  >
                    <Text variant="caption" weight="600" style={{ color: colors.textInverse }}>
                      {selectedImageIndex + 1} / {validImages.length}
                    </Text>
                  </View>
                  <View
                    style={{
                      position: 'absolute',
                      bottom: spacing.md,
                      left: 0,
                      right: 0,
                      flexDirection: 'row',
                      justifyContent: 'center',
                      gap: 5,
                    }}
                  >
                    {validImages.map((_, i) => (
                      <View
                        key={i}
                        style={{
                          width: i === selectedImageIndex ? 16 : 6,
                          height: 6,
                          borderRadius: 3,
                          backgroundColor: colors.textInverse,
                          opacity: i === selectedImageIndex ? 1 : 0.5,
                        }}
                      />
                    ))}
                  </View>
                </>
              )}
            </>
          ) : (
            <View
              style={{
                flex: 1,
                justifyContent: 'center',
                alignItems: 'center',
                backgroundColor: colors.surfaceAlt,
              }}
            >
              <View
                style={{
                  width: 88,
                  height: 88,
                  borderRadius: radius.pill,
                  backgroundColor: colors.accentMuted,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.lg,
                }}
              >
                <Icon name="image" size={48} color={colors.accent} />
              </View>
              <Text variant="bodyMed" weight="600" color="textSecondary" align="center">
                No photos yet
              </Text>
              <Text variant="bodySm" color="textMuted" align="center" style={{ marginTop: spacing.xs }}>
                The seller hasn&apos;t added photos for this product
              </Text>
            </View>
          )}
        </View>

        {/* ===== Name, price, stock, rating ===== */}
        <Card padding="lg" style={{ borderRadius: 0, gap: spacing.xs }}>
          {categoryName && (
            <Text variant="overline" color="textMuted" style={{ textTransform: 'uppercase' }}>
              {categoryName}
            </Text>
          )}
          <Text variant="title3" weight="700">
            {product.name}
          </Text>
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginTop: spacing.xs,
            }}
          >
            <Text variant="title1" weight="800" color="accent" numeric>
              {formatPrice(product.price)}
            </Text>
            <View
              style={{
                paddingHorizontal: spacing.sm,
                paddingVertical: 3,
                borderRadius: radius.pill,
                backgroundColor: isInStock ? colors.successBg : colors.dangerBg,
              }}
            >
              <Text variant="caption" weight="600" color={isInStock ? 'success' : 'danger'}>
                {!isInStock
                  ? 'Out of stock'
                  : product.stock <= LOW_STOCK
                    ? `${product.stock} left`
                    : `${product.stock} in stock`}
              </Text>
            </View>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            <Icon name="star" size={13} color={ratingCount > 0 ? colors.warning : colors.textMuted} />
            <Text variant="caption" color="textMuted">
              {ratingCount > 0
                ? `${product.rating!.average.toFixed(1)} · ${ratingCount} ${ratingCount === 1 ? 'review' : 'reviews'}`
                : 'No reviews yet'}
              {product.sales > 0 ? ` · ${product.sales} sold` : ''}
            </Text>
          </View>
        </Card>

        {/* ===== The buyer's choices: options, quantity, note ===== */}
        {isInStock && (
          <Card padding="lg" style={section}>
            {product.attributes?.map((attr, i) => {
              const options = parseSpecOptions(attr.value);
              const picked = selectedSpecs[attr.name] ?? [];
              return (
                <View key={i} style={{ marginBottom: spacing.lg }}>
                  <Text variant="bodySm" weight="600" style={{ marginBottom: spacing.sm }}>
                    {attr.name}
                  </Text>
                  {options.length > 0 ? (
                    <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                      {options.map((opt) => (
                        <Chip
                          key={opt}
                          label={opt}
                          selected={picked.includes(opt)}
                          onPress={() => toggleSpec(attr.name, opt)}
                        />
                      ))}
                    </View>
                  ) : (
                    <Text variant="bodySm" color="textSecondary">
                      {attr.value}
                    </Text>
                  )}
                </View>
              );
            })}

            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <View>
                <Text variant="bodySm" weight="600">
                  Quantity
                </Text>
                <Text variant="caption" color="textMuted">
                  {product.stock} available
                </Text>
              </View>
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  borderWidth: 1,
                  borderColor: colors.border,
                  borderRadius: radius.md,
                }}
              >
                <TouchableOpacity
                  accessibilityLabel="Decrease quantity"
                  style={stepButton(quantity > 1)}
                  onPress={() => changeQuantity(Math.max(1, quantity - 1))}
                  disabled={quantity <= 1}
                >
                  <Icon name="minus" size={18} color={colors.accent} />
                </TouchableOpacity>
                <Text variant="bodyMed" weight="700" numeric style={{ minWidth: 32, textAlign: 'center' }}>
                  {quantity}
                </Text>
                <TouchableOpacity
                  accessibilityLabel="Increase quantity"
                  style={stepButton(quantity < product.stock)}
                  onPress={() => changeQuantity(Math.min(product.stock, quantity + 1))}
                  disabled={quantity >= product.stock}
                >
                  <Icon name="plus" size={18} color={colors.accent} />
                </TouchableOpacity>
              </View>
            </View>

            <View
              style={{
                marginTop: spacing.md,
                paddingTop: spacing.md,
                borderTopWidth: 1,
                borderTopColor: colors.hairline,
              }}
            >
              {showNote ? (
                <Input
                  label="Note for the seller (optional)"
                  value={purchaseNotes}
                  onChangeText={changeNotes}
                  placeholder="e.g. 2 white size 42, 1 blue size 43"
                  multiline
                  numberOfLines={3}
                  maxLength={500}
                  counter
                  autoFocus={noteOpen && purchaseNotes.length === 0}
                  inputStyle={{ minHeight: 72, textAlignVertical: 'top' }}
                />
              ) : (
                <PressableScale
                  onPress={() => setNoteOpen(true)}
                  accessibilityRole="button"
                  style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}
                >
                  <Icon name="edit" size={16} color={colors.textSecondary} />
                  <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                    Add a note for the seller
                  </Text>
                  <Text variant="bodySm" weight="600" color="accent">
                    Add
                  </Text>
                </PressableScale>
              )}
            </View>
          </Card>
        )}

        {/* ===== Seller ===== */}
        <PressableScale
          onPress={handleContactSeller}
          accessibilityRole="button"
          accessibilityLabel={`View ${product.companyName || 'seller'}'s shop`}
        >
          <Card padding="lg" style={section}>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Avatar name={product.companyName || product.email || 'S'} size={44} />
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodyMed" weight="600" numberOfLines={1}>
                  {product.companyName || 'Seller'}
                </Text>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: 2 }}>
                  <Icon name="nearMe" size={12} color={colors.textMuted} />
                  <Text variant="caption" color="textMuted" numberOfLines={1} style={{ flex: 1 }}>
                    {product.location || 'Location not set'}
                  </Text>
                </View>
              </View>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Text variant="bodySm" weight="600" color="accent">
                  View shop
                </Text>
                <Icon name="chevronRight" size={16} color={colors.accent} />
              </View>
            </View>
          </Card>
        </PressableScale>

        {/* ===== Description ===== */}
        {description ? (
          <Card padding="lg" style={section}>
            <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
              Description
            </Text>
            <Text
              variant="bodySm"
              color="textSecondary"
              style={{ lineHeight: 22 }}
              numberOfLines={descriptionClamped ? DESCRIPTION_CLAMP_LINES : undefined}
            >
              {description}
            </Text>
            {description.length > DESCRIPTION_CLAMP_CHARS && (
              <PressableScale
                onPress={() => setDescriptionOpen((open) => !open)}
                hitSlop={8}
                style={{ marginTop: spacing.sm, alignSelf: 'flex-start' }}
              >
                <Text variant="bodySm" weight="600" color="accent">
                  {descriptionOpen ? 'Show less' : 'Read more'}
                </Text>
              </PressableScale>
            )}
          </Card>
        ) : null}

        {/* ===== Delivery ===== */}
        <Card padding="lg" style={section}>
          <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
            Delivery
          </Text>
          {deliveryRows
            .filter((row): row is typeof row & { fee: number } => typeof row.fee === 'number')
            .map((row) => (
              <View
                key={row.label}
                style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}
              >
                <Icon name={row.icon} size={16} color={colors.textMuted} />
                <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                  {row.label}
                </Text>
                {row.fee <= 0 ? (
                  <Text variant="bodySm" weight="600" color="success">
                    Free
                  </Text>
                ) : (
                  <Text variant="bodySm" weight="600" numeric>
                    {formatPrice(row.fee)}
                  </Text>
                )}
              </View>
            ))}
        </Card>

        {/* ===== Meta ===== */}
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md }}>
          <Text variant="caption" color="textMuted">
            Listed {new Date(product.createdAt).toLocaleDateString()} · {product.views || 0} views
          </Text>
        </View>
      </ScrollView>

      {/* ===== Action bar (sits above the system nav / home indicator) ===== */}
      <BottomActionBar>
        {isInStock ? (
          <>
            {inCartQty > 0 && (
              <PressableScale
                onPress={() => router.push('/cart')}
                accessibilityRole="button"
                accessibilityLabel={`${inCartQty} in your cart. View cart`}
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
              >
                <Icon name="checkCircle" size={15} color={colors.success} />
                <Text variant="caption" weight="600" color="success" style={{ flex: 1 }}>
                  {inCartQty} in your cart
                </Text>
                <Text variant="caption" weight="600" color="success" style={{ textDecorationLine: 'underline' }}>
                  View cart
                </Text>
              </PressableScale>
            )}
            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <Button
                title={cartHoldsAllStock ? 'All in cart' : 'Add to cart'}
                variant="secondary"
                leftIcon="cart"
                onPress={handleAddToCart}
                loading={pendingAction === 'add'}
                disabled={cartHoldsAllStock || pendingAction !== null}
                fullWidth={false}
                style={{ flex: 1 }}
              />
              <Button
                title="Buy now"
                subtitle={formatPrice(product.price * quantity)}
                onPress={handleBuyNow}
                loading={pendingAction === 'buy'}
                disabled={pendingAction !== null}
                fullWidth={false}
                style={{ flex: 1.15 }}
              />
            </View>
          </>
        ) : (
          <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.sm }}>
              <Icon name="info" size={18} color={colors.danger} />
              <Text variant="bodySm" weight="500" color="danger">
                This product is currently out of stock
              </Text>
            </View>
          </AccentCard>
        )}
      </BottomActionBar>
    </SafeAreaView>
  );
}
