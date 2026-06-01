// app/(dashboard)/(tabs)/(home)/product/[productId].tsx
//
// Product detail (home tab) — gallery, info, seller, shipping, quantity, and the
// add-to-cart / buy-now action bar. Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives).
//
// MARKETPLACE screen: the product fetch (productData param → api.getProductById),
// the service-redirect bounce, qty/variant selection bounds, add-to-cart
// (cartService + cart-count), buy-now / checkout navigation, contact-seller
// navigation, and the currency formatting are all preserved verbatim — this is a
// visual / structural migration only.
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  ScrollView,
  Image,
  TouchableOpacity,
  Alert,
  Dimensions,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import api, { Product } from '@/components/services/api';
import { cartService } from '@/components/services/cart';
import { useAuth } from '@/AuthContext/AuthContext';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import { useTheme } from '@/theme';
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
  Text,
} from '@/components/ui';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const IMAGE_HEIGHT = SCREEN_WIDTH * 0.85;

export default function ProductDetailsScreen() {
  const { productId, productData: productDataParam } = useLocalSearchParams<{
    productId: string;
    productData?: string;
  }>();
  const router = useRouter();
  const { colors, spacing, radius } = useTheme();

  const [product, setProduct] = useState<Product | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { token } = useAuth();
  const [quantity, setQuantity] = useState(1);
  const [selectedImageIndex, setSelectedImageIndex] = useState(0);
  const [cartCount, setCartCount] = useState(0);
  const { currency, rate } = useCurrency();

  const formatPrice = (price: number) =>
    formatPriceUtil(price, { currency, rate });

  const loadCartCount = useCallback(async () => {
    if (!token) return;
    try {
      const count = await cartService.getCartItemCount();
      setCartCount(count);
    } catch {}
  }, [token]);

  useEffect(() => {
    if (token) {
      api.setToken(token);
      cartService.setToken(token);
    }
    loadProduct();
    loadCartCount();
  }, [productId, token]);

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

  const handleAddToCart = async () => {
    if (!token) {
      Alert.alert('Login Required', 'Please login to add items to your cart', [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Login', onPress: () => router.push('/login') },
      ]);
      return;
    }
    if (!product) return;

    try {
      const cartResponse = await cartService.addToCart({
        productId: product._id || product.id || '',
        quantity,
      });
      if (cartResponse.success) {
        loadCartCount();
        Alert.alert('Added to Cart', `${product.name} x${quantity} added`, [
          { text: 'View Cart', onPress: () => router.push('/cart') },
          { text: 'Continue', style: 'cancel' },
        ]);
      } else {
        Alert.alert('Error', cartResponse.message || 'Failed to add to cart');
      }
    } catch (error: any) {
      Alert.alert('Error', error.message || 'Failed to add item to cart');
    }
  };

  const handleBuyNow = () => {
    if (!token) {
      Alert.alert('Login Required', 'Please login to continue', [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Login', onPress: () => router.push('/login') },
      ]);
      return;
    }
    if (!product) return;
    router.push(
      `/checkout?productId=${product._id || product.id}&quantity=${quantity}`,
    );
  };

  const handleContactSeller = () => {
    if (!product) return;
    router.push(
      `/seller/${product._id || product.id}?sellerEmail=${encodeURIComponent(product.email)}`,
    );
  };

  // formatPrice is now defined via useCurrency above

  const getCategoryName = () => {
    if (!product?.category) return product?.categoryName || null;
    if (typeof product.category === 'string')
      return product.categoryName || null;
    return product.category.title || product.categoryName || null;
  };

  // Header cart button — preserves the cartCount state + /cart navigation.
  const headerCartButton = () => (
    <TouchableOpacity
      onPress={() => router.push('/cart')}
      activeOpacity={0.7}
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
        <Stack.Screen options={{ headerShown: false }} />
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
        <Stack.Screen
          options={{
            headerShown: true,
            headerTitle: '',
            headerBackTitle: 'Back',
            headerTintColor: colors.accent,
            headerStyle: { backgroundColor: colors.surface },
            headerShadowVisible: false,
          }}
        />
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
            <PressableScale onPress={() => router.back()} hitSlop={8} style={{ paddingVertical: spacing.md }}>
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

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen
        options={{
          headerShown: true,
          headerTitle: '',
          headerBackTitle: 'Back',
          headerTintColor: colors.accent,
          headerStyle: { backgroundColor: colors.surface },
          headerShadowVisible: false,
          headerRight: headerCartButton,
        }}
      />

      <ScrollView showsVerticalScrollIndicator={false} bounces={false}>
        {/* ===== Image Section ===== */}
        <View
          style={{
            backgroundColor: colors.surface,
            width: '100%',
            height: IMAGE_HEIGHT,
            justifyContent: 'center',
            alignItems: 'center',
          }}
        >
          {validImages.length > 0 ? (
            <>
              <Image
                source={{ uri: validImages[selectedImageIndex]?.url }}
                style={{ width: '100%', height: '100%' }}
                resizeMode="cover"
              />
              {validImages.length > 1 && (
                <View
                  style={{
                    position: 'absolute',
                    bottom: spacing.md,
                    right: spacing.lg,
                    backgroundColor: colors.overlay,
                    borderRadius: radius.md,
                    paddingHorizontal: spacing.md,
                    paddingVertical: spacing.xs,
                  }}
                >
                  <Text variant="caption" weight="600" style={{ color: colors.textInverse }}>
                    {selectedImageIndex + 1}/{validImages.length}
                  </Text>
                </View>
              )}
            </>
          ) : (
            <View
              style={{
                flex: 1,
                justifyContent: 'center',
                alignItems: 'center',
                backgroundColor: colors.surfaceAlt,
                width: '100%',
                overflow: 'hidden',
              }}
            >
              <View
                style={{
                  justifyContent: 'center',
                  alignItems: 'center',
                  paddingHorizontal: spacing['3xl'],
                  paddingVertical: spacing['2xl'],
                  borderWidth: 1.5,
                  borderColor: colors.accentMuted,
                  borderStyle: 'dashed',
                  borderRadius: radius.lg,
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
                  No Image Available
                </Text>
                <Text
                  variant="bodySm"
                  color="textMuted"
                  align="center"
                  style={{ maxWidth: 220, marginTop: spacing.xs }}
                >
                  The seller hasn&apos;t added photos for this product
                </Text>
              </View>
              <Text
                variant="overline"
                color="textMuted"
                style={{
                  position: 'absolute',
                  bottom: spacing.lg,
                  letterSpacing: 4,
                  textTransform: 'uppercase',
                  opacity: 0.4,
                }}
              >
                TSA CONNECT
              </Text>
            </View>
          )}
        </View>

        {/* Thumbnails */}
        {validImages.length > 1 && (
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={{
              paddingHorizontal: spacing.lg,
              paddingVertical: spacing.md,
            }}
            style={{ backgroundColor: colors.surface }}
          >
            {validImages.map((img, i) => (
              <TouchableOpacity
                key={i}
                onPress={() => setSelectedImageIndex(i)}
                style={{
                  width: 56,
                  height: 56,
                  borderRadius: radius.sm,
                  marginRight: spacing.md,
                  borderWidth: 2,
                  borderColor:
                    selectedImageIndex === i ? colors.accent : 'transparent',
                  overflow: 'hidden',
                }}
              >
                <Image
                  source={{ uri: img.url }}
                  style={{ width: '100%', height: '100%' }}
                  resizeMode="cover"
                />
              </TouchableOpacity>
            ))}
          </ScrollView>
        )}

        {/* ===== Product Info Card ===== */}
        <Card
          padding="xl"
          style={{ marginTop: spacing.sm, borderRadius: 0 }}
        >
          {/* Price Row */}
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginBottom: spacing.md,
            }}
          >
            <Text variant="title1" weight="800" color="accent" numeric>
              {formatPrice(product.price)}
            </Text>
            <Badge
              label={isInStock ? `${product.stock} in stock` : 'Out of stock'}
              icon={isInStock ? 'checkCircle' : 'closeCircle'}
              color={isInStock ? colors.success : colors.danger}
              background={isInStock ? colors.successBg : colors.dangerBg}
            />
          </View>

          {/* Name */}
          <Text variant="title3" weight="600" style={{ marginBottom: spacing.md }}>
            {product.name}
          </Text>

          {/* Category & Rating Row */}
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            {categoryName && (
              <Badge
                label={categoryName}
                icon="pricetag"
                color={colors.accent}
                background={colors.accentMuted}
                size="sm"
              />
            )}
            <Badge
              label={`${product.rating?.average?.toFixed(1) || '0.0'} (${product.rating?.count || 0})`}
              icon="star"
              color={colors.warning}
              background={colors.warningBg}
              size="sm"
            />
          </View>
        </Card>

        {/* ===== Seller Card ===== */}
        <PressableScale onPress={handleContactSeller}>
          <Card
            padding="lg"
            style={{ marginTop: spacing.sm, borderRadius: 0 }}
          >
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
                  Visit
                </Text>
                <Icon name="chevronRight" size={16} color={colors.accent} />
              </View>
            </View>
          </Card>
        </PressableScale>

        {/* ===== Description ===== */}
        {product.description ? (
          <Card padding="lg" style={{ marginTop: spacing.sm, borderRadius: 0 }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
              Description
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ lineHeight: 22 }}>
              {product.description}
            </Text>
          </Card>
        ) : null}

        {/* ===== Specifications ===== */}
        {product.attributes && product.attributes.length > 0 && (
          <Card padding="lg" style={{ marginTop: spacing.sm, borderRadius: 0 }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
              Specifications
            </Text>
            {product.attributes.map((attr, i) => (
              <View key={i}>
                <View
                  style={{
                    flexDirection: 'row',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    paddingVertical: spacing.md,
                  }}
                >
                  <Text variant="bodySm" color="textMuted">
                    {attr.name}
                  </Text>
                  <Text variant="bodySm" weight="500">
                    {attr.value}
                  </Text>
                </View>
                {i < product.attributes!.length - 1 && <Divider />}
              </View>
            ))}
          </Card>
        )}

        {/* ===== Shipping Info ===== */}
        <Card padding="lg" style={{ marginTop: spacing.sm, borderRadius: 0 }}>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
            Shipping
          </Text>
          <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}>
            <Icon name="home" size={16} color={colors.textMuted} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Same Residential Area
            </Text>
            <Text variant="bodySm" weight="600" color="success">
              Free
            </Text>
          </View>
          {(product.shippingSameCity ?? 0) > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}>
              <Icon name="nearMe" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                Same City
              </Text>
              <Text variant="bodySm" weight="600" numeric>
                ${product.shippingSameCity}
              </Text>
            </View>
          )}
          {(product.shippingSameState ?? 0) > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}>
              <Icon name="nearMe" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                Same State
              </Text>
              <Text variant="bodySm" weight="600" numeric>
                ${product.shippingSameState}
              </Text>
            </View>
          )}
          {(product.shippingSameCountry ?? 0) > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}>
              <Icon name="send" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                Nationwide
              </Text>
              <Text variant="bodySm" weight="600" numeric>
                ${product.shippingSameCountry}
              </Text>
            </View>
          )}
          {(product.shippingInternational ?? 0) > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm, gap: spacing.sm }}>
              <Icon name="send" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                International
              </Text>
              <Text variant="bodySm" weight="600" numeric>
                ${product.shippingInternational}
              </Text>
            </View>
          )}
        </Card>

        {/* ===== Quantity Selector ===== */}
        {isInStock && (
          <Card padding="lg" style={{ marginTop: spacing.sm, borderRadius: 0 }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
              Quantity
            </Text>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <TouchableOpacity
                style={{
                  width: 38,
                  height: 38,
                  borderRadius: radius.pill,
                  borderWidth: 1.5,
                  borderColor: quantity <= 1 ? colors.border : colors.accent,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
                onPress={() => setQuantity(Math.max(1, quantity - 1))}
                disabled={quantity <= 1}
              >
                <Icon name="minus" size={18} color={quantity <= 1 ? colors.textDisabled : colors.accent} />
              </TouchableOpacity>
              <Text variant="title3" weight="700" style={{ marginHorizontal: spacing['2xl'] }}>
                {quantity}
              </Text>
              <TouchableOpacity
                style={{
                  width: 38,
                  height: 38,
                  borderRadius: radius.pill,
                  borderWidth: 1.5,
                  borderColor: quantity >= product.stock ? colors.border : colors.accent,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
                onPress={() => setQuantity(Math.min(product.stock, quantity + 1))}
                disabled={quantity >= product.stock}
              >
                <Icon name="plus" size={18} color={quantity >= product.stock ? colors.textDisabled : colors.accent} />
              </TouchableOpacity>
              <Text variant="bodySm" color="textMuted" style={{ marginLeft: spacing.md }}>
                of {product.stock}
              </Text>
            </View>
          </Card>
        )}

        {/* ===== Meta ===== */}
        <View
          style={{
            flexDirection: 'row',
            flexWrap: 'wrap',
            gap: spacing.lg,
            paddingHorizontal: spacing.xl,
            paddingVertical: spacing.md,
            marginTop: spacing.sm,
          }}
        >
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            <Icon name="clock" size={13} color={colors.textMuted} />
            <Text variant="caption" color="textMuted">
              Listed {new Date(product.createdAt).toLocaleDateString()}
            </Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            <Icon name="eye" size={13} color={colors.textMuted} />
            <Text variant="caption" color="textMuted">
              {product.views || 0} views
            </Text>
          </View>
          {product.sales > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
              <Icon name="cart" size={13} color={colors.textMuted} />
              <Text variant="caption" color="textMuted">
                {product.sales} sold
              </Text>
            </View>
          )}
        </View>

        <View style={{ height: 100 }} />
      </ScrollView>

      {/* ===== Bottom Action Bar ===== */}
      <View
        style={{
          position: 'absolute',
          bottom: 0,
          left: 0,
          right: 0,
          flexDirection: 'row',
          alignItems: 'center',
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: spacing['2xl'],
          backgroundColor: colors.surface,
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
          gap: spacing.md,
        }}
      >
        {isInStock ? (
          <>
            <TouchableOpacity
              onPress={handleContactSeller}
              style={{
                width: 48,
                height: 48,
                borderRadius: radius.md,
                borderWidth: 1.5,
                borderColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="send" size={20} color={colors.accent} />
            </TouchableOpacity>
            <Button
              title="Add to Cart"
              variant="secondary"
              leftIcon="cart"
              onPress={handleAddToCart}
              style={{ flex: 1 }}
            />
            <TouchableOpacity
              onPress={handleBuyNow}
              style={{
                flex: 1.2,
                height: 48,
                justifyContent: 'center',
                alignItems: 'center',
                borderRadius: radius.md,
                backgroundColor: colors.accent,
              }}
            >
              <Text variant="bodySm" weight="700" style={{ color: colors.onAccent }}>
                Buy Now
              </Text>
              <Text variant="caption" numeric style={{ color: colors.onAccent, opacity: 0.85, marginTop: 1 }}>
                {formatPrice(product.price * quantity)}
              </Text>
            </TouchableOpacity>
          </>
        ) : (
          <AccentCard
            railColor={colors.danger}
            padding="md"
            style={{ flex: 1, backgroundColor: colors.dangerBg }}
          >
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.sm }}>
              <Icon name="info" size={18} color={colors.danger} />
              <Text variant="bodySm" weight="500" color="danger">
                This product is currently out of stock
              </Text>
            </View>
          </AccentCard>
        )}
      </View>
    </SafeAreaView>
  );
}
