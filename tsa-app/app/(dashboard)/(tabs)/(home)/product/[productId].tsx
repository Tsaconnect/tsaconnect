// app/(dashboard)/(tabs)/(home)/product/[productId].tsx
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  Image,
  TouchableOpacity,
  StatusBar,
  ActivityIndicator,
  Alert,
  Dimensions,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import api, { Product } from '@/components/services/api';
import { cartService } from '@/components/services/cart';
import { useAuth } from '@/AuthContext/AuthContext';
import { SafeAreaView } from 'react-native-safe-area-context';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const IMAGE_HEIGHT = SCREEN_WIDTH * 0.85;

export default function ProductDetailsScreen() {
  const { productId, productData: productDataParam } = useLocalSearchParams<{
    productId: string;
    productData?: string;
  }>();
  const router = useRouter();

  const [product, setProduct] = useState<Product | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { token } = useAuth();
  const [quantity, setQuantity] = useState(1);
  const [selectedImageIndex, setSelectedImageIndex] = useState(0);
  const [cartCount, setCartCount] = useState(0);

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

  const loadProduct = async () => {
    if (!productId) return;

    if (productDataParam) {
      try {
        const parsed = JSON.parse(productDataParam as string);
        if (!parsed._id && parsed.id) parsed._id = parsed.id;
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

  const formatPrice = (price: number) =>
    `$${price.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;

  const getCategoryName = () => {
    if (!product?.category) return product?.categoryName || null;
    if (typeof product.category === 'string')
      return product.categoryName || null;
    return product.category.title || product.categoryName || null;
  };

  // --- Loading ---
  if (loading) {
    return (
      <SafeAreaView style={styles.safeArea}>
        <Stack.Screen options={{ headerShown: false }} />
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color="#D4AF37" />
        </View>
      </SafeAreaView>
    );
  }

  // --- Error ---
  if (error || !product) {
    return (
      <SafeAreaView style={styles.safeArea}>
        <Stack.Screen
          options={{
            headerShown: true,
            headerTitle: '',
            headerBackTitle: 'Back',
            headerTintColor: '#D4AF37',
            headerStyle: { backgroundColor: '#FFF' },
            headerShadowVisible: false,
          }}
        />
        <View style={styles.errorContainer}>
          <View style={styles.errorIcon}>
            <Ionicons name="bag-remove-outline" size={48} color="#D4AF37" />
          </View>
          <Text style={styles.errorTitle}>Product Not Found</Text>
          <Text style={styles.errorText}>
            {error ||
              'This product may have been removed or is no longer available.'}
          </Text>
          <TouchableOpacity style={styles.retryButton} onPress={loadProduct}>
            <Text style={styles.retryButtonText}>Try Again</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={styles.goBackButton}
            onPress={() => router.back()}
          >
            <Text style={styles.goBackText}>Go Back</Text>
          </TouchableOpacity>
        </View>
      </SafeAreaView>
    );
  }

  const validImages = getValidImages();
  const categoryName = getCategoryName();
  const isInStock = product.stock > 0;

  return (
    <SafeAreaView style={styles.safeArea}>
      <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

      <Stack.Screen
        options={{
          headerShown: true,
          headerTitle: '',
          headerBackTitle: 'Back',
          headerTintColor: '#D4AF37',
          headerStyle: { backgroundColor: '#FFF' },
          headerShadowVisible: false,
          headerRight: () => (
            <TouchableOpacity
              style={styles.headerCartBtn}
              onPress={() => router.push('/cart')}
            >
              <Ionicons name="cart-outline" size={22} color="#D4AF37" />
              {cartCount > 0 && (
                <View style={styles.cartBadge}>
                  <Text style={styles.cartBadgeText}>
                    {cartCount > 99 ? '99+' : cartCount}
                  </Text>
                </View>
              )}
            </TouchableOpacity>
          ),
        }}
      />

      <ScrollView showsVerticalScrollIndicator={false} bounces={false}>
        {/* ===== Image Section ===== */}
        <View style={styles.imageSection}>
          {validImages.length > 0 ? (
            <>
              <Image
                source={{ uri: validImages[selectedImageIndex]?.url }}
                style={styles.mainImage}
                resizeMode="cover"
              />
              {validImages.length > 1 && (
                <View style={styles.imageIndicator}>
                  <Text style={styles.imageIndicatorText}>
                    {selectedImageIndex + 1}/{validImages.length}
                  </Text>
                </View>
              )}
            </>
          ) : (
            <View style={styles.noImageContainer}>
              <View style={styles.noImageBorder}>
                <View style={styles.noImageIconCircle}>
                  <Ionicons name="image-outline" size={48} color="#D4AF37" />
                </View>
                <Text style={styles.noImageTitle}>No Image Available</Text>
                <Text style={styles.noImageSubtitle}>
                  The seller hasn't added photos for this product
                </Text>
              </View>
              <Text style={styles.noImageWatermark}>TSA CONNECT</Text>
            </View>
          )}
        </View>

        {/* Thumbnails */}
        {validImages.length > 1 && (
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.thumbnailRow}
          >
            {validImages.map((img, i) => (
              <TouchableOpacity
                key={i}
                onPress={() => setSelectedImageIndex(i)}
                style={[
                  styles.thumb,
                  selectedImageIndex === i && styles.thumbActive,
                ]}
              >
                <Image
                  source={{ uri: img.url }}
                  style={styles.thumbImg}
                  resizeMode="cover"
                />
              </TouchableOpacity>
            ))}
          </ScrollView>
        )}

        {/* ===== Product Info Card ===== */}
        <View style={styles.infoCard}>
          {/* Price Row */}
          <View style={styles.priceRow}>
            <Text style={styles.price}>{formatPrice(product.price)}</Text>
            <View
              style={[
                styles.stockBadge,
                isInStock ? styles.stockIn : styles.stockOut,
              ]}
            >
              <Ionicons
                name={isInStock ? 'checkmark-circle' : 'close-circle'}
                size={14}
                color={isInStock ? '#16A34A' : '#DC2626'}
              />
              <Text
                style={[
                  styles.stockText,
                  isInStock ? styles.stockTextIn : styles.stockTextOut,
                ]}
              >
                {isInStock ? `${product.stock} in stock` : 'Out of stock'}
              </Text>
            </View>
          </View>

          {/* Name */}
          <Text style={styles.productName}>{product.name}</Text>

          {/* Category & Rating Row */}
          <View style={styles.metaRow}>
            {categoryName && (
              <View style={styles.categoryPill}>
                <Ionicons name="pricetag-outline" size={12} color="#8B6914" />
                <Text style={styles.categoryText}>{categoryName}</Text>
              </View>
            )}
            <View style={styles.ratingPill}>
              <Ionicons name="star" size={12} color="#F59E0B" />
              <Text style={styles.ratingText}>
                {product.rating?.average?.toFixed(1) || '0.0'}
              </Text>
              <Text style={styles.reviewCount}>
                ({product.rating?.count || 0})
              </Text>
            </View>
          </View>
        </View>

        {/* ===== Seller Card ===== */}
        <TouchableOpacity
          style={styles.sellerCard}
          onPress={handleContactSeller}
          activeOpacity={0.7}
        >
          <View style={styles.sellerAvatar}>
            <Text style={styles.sellerInitial}>
              {(product.companyName || product.email || 'S')
                .charAt(0)
                .toUpperCase()}
            </Text>
          </View>
          <View style={styles.sellerInfo}>
            <Text style={styles.sellerName} numberOfLines={1}>
              {product.companyName || 'Seller'}
            </Text>
            <View style={styles.sellerMeta}>
              <Ionicons name="location-outline" size={12} color="#888" />
              <Text style={styles.sellerMetaText} numberOfLines={1}>
                {product.location || 'Location not set'}
              </Text>
            </View>
          </View>
          <View style={styles.sellerCta}>
            <Text style={styles.sellerCtaText}>Visit</Text>
            <Ionicons name="chevron-forward" size={16} color="#D4AF37" />
          </View>
        </TouchableOpacity>

        {/* ===== Description ===== */}
        {product.description ? (
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Description</Text>
            <Text style={styles.description}>{product.description}</Text>
          </View>
        ) : null}

        {/* ===== Specifications ===== */}
        {product.attributes && product.attributes.length > 0 && (
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Specifications</Text>
            {product.attributes.map((attr, i) => (
              <View
                key={i}
                style={[
                  styles.specRow,
                  i === product.attributes!.length - 1 && {
                    borderBottomWidth: 0,
                  },
                ]}
              >
                <Text style={styles.specLabel}>{attr.name}</Text>
                <Text style={styles.specValue}>{attr.value}</Text>
              </View>
            ))}
          </View>
        )}

        {/* ===== Shipping Info ===== */}
        {((product.shippingSameCity ?? 0) > 0 ||
          (product.shippingSameState ?? 0) > 0 ||
          (product.shippingSameCountry ?? 0) > 0 ||
          (product.shippingInternational ?? 0) > 0) && (
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Shipping</Text>
            {(product.shippingSameCity ?? 0) > 0 && (
              <View style={styles.shippingRow}>
                <Ionicons name="bicycle-outline" size={16} color="#888" />
                <Text style={styles.shippingLabel}>Same City</Text>
                <Text style={styles.shippingPrice}>
                  ${product.shippingSameCity}
                </Text>
              </View>
            )}
            {(product.shippingSameState ?? 0) > 0 && (
              <View style={styles.shippingRow}>
                <Ionicons name="car-outline" size={16} color="#888" />
                <Text style={styles.shippingLabel}>Same State</Text>
                <Text style={styles.shippingPrice}>
                  ${product.shippingSameState}
                </Text>
              </View>
            )}
            {(product.shippingSameCountry ?? 0) > 0 && (
              <View style={styles.shippingRow}>
                <Ionicons name="bus-outline" size={16} color="#888" />
                <Text style={styles.shippingLabel}>Nationwide</Text>
                <Text style={styles.shippingPrice}>
                  ${product.shippingSameCountry}
                </Text>
              </View>
            )}
            {(product.shippingInternational ?? 0) > 0 && (
              <View style={styles.shippingRow}>
                <Ionicons name="airplane-outline" size={16} color="#888" />
                <Text style={styles.shippingLabel}>International</Text>
                <Text style={styles.shippingPrice}>
                  ${product.shippingInternational}
                </Text>
              </View>
            )}
          </View>
        )}

        {/* ===== Quantity Selector ===== */}
        {isInStock && (
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Quantity</Text>
            <View style={styles.quantityRow}>
              <TouchableOpacity
                style={[styles.qtyBtn, quantity <= 1 && styles.qtyBtnDisabled]}
                onPress={() => setQuantity(Math.max(1, quantity - 1))}
                disabled={quantity <= 1}
              >
                <Ionicons
                  name="remove"
                  size={18}
                  color={quantity <= 1 ? '#CCC' : '#D4AF37'}
                />
              </TouchableOpacity>
              <Text style={styles.qtyValue}>{quantity}</Text>
              <TouchableOpacity
                style={[
                  styles.qtyBtn,
                  quantity >= product.stock && styles.qtyBtnDisabled,
                ]}
                onPress={() =>
                  setQuantity(Math.min(product.stock, quantity + 1))
                }
                disabled={quantity >= product.stock}
              >
                <Ionicons
                  name="add"
                  size={18}
                  color={quantity >= product.stock ? '#CCC' : '#D4AF37'}
                />
              </TouchableOpacity>
              <Text style={styles.qtyMax}>of {product.stock}</Text>
            </View>
          </View>
        )}

        {/* ===== Meta ===== */}
        <View style={styles.metaFooter}>
          <View style={styles.metaItem}>
            <Ionicons name="time-outline" size={13} color="#AAA" />
            <Text style={styles.metaText}>
              Listed {new Date(product.createdAt).toLocaleDateString()}
            </Text>
          </View>
          <View style={styles.metaItem}>
            <Ionicons name="eye-outline" size={13} color="#AAA" />
            <Text style={styles.metaText}>{product.views || 0} views</Text>
          </View>
          {product.sales > 0 && (
            <View style={styles.metaItem}>
              <Ionicons name="bag-check-outline" size={13} color="#AAA" />
              <Text style={styles.metaText}>{product.sales} sold</Text>
            </View>
          )}
        </View>

        <View style={{ height: 100 }} />
      </ScrollView>

      {/* ===== Bottom Action Bar ===== */}
      <View style={styles.bottomBar}>
        {isInStock ? (
          <>
            <TouchableOpacity
              style={styles.contactBtn}
              onPress={handleContactSeller}
            >
              <Ionicons name="chatbubble-outline" size={20} color="#D4AF37" />
            </TouchableOpacity>
            <TouchableOpacity style={styles.cartBtn} onPress={handleAddToCart}>
              <Ionicons name="cart-outline" size={18} color="#D4AF37" />
              <Text style={styles.cartBtnText}>Add to Cart</Text>
            </TouchableOpacity>
            <TouchableOpacity style={styles.buyBtn} onPress={handleBuyNow}>
              <Text style={styles.buyBtnText}>Buy Now</Text>
              <Text style={styles.buyBtnPrice}>
                {formatPrice(product.price * quantity)}
              </Text>
            </TouchableOpacity>
          </>
        ) : (
          <View style={styles.oosBar}>
            <Ionicons
              name="information-circle-outline"
              size={18}
              color="#DC2626"
            />
            <Text style={styles.oosText}>
              This product is currently out of stock
            </Text>
          </View>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: { flex: 1, backgroundColor: '#F5F5F7' },
  loadingContainer: { flex: 1, justifyContent: 'center', alignItems: 'center' },
  headerCartBtn: { marginRight: 8, padding: 6, position: 'relative' },
  cartBadge: {
    position: 'absolute',
    top: 0,
    right: 0,
    backgroundColor: '#DC2626',
    borderRadius: 10,
    minWidth: 18,
    height: 18,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 4,
    borderWidth: 1.5,
    borderColor: '#FFF',
  },
  cartBadgeText: {
    color: '#FFF',
    fontSize: 10,
    fontWeight: '700',
    lineHeight: 12,
  },

  // Error
  errorContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 32,
  },
  errorIcon: {
    width: 88,
    height: 88,
    borderRadius: 44,
    backgroundColor: 'rgba(212,175,55,0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 20,
  },
  errorTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#1A1A1A',
    marginBottom: 8,
  },
  errorText: {
    fontSize: 14,
    color: '#888',
    textAlign: 'center',
    lineHeight: 20,
    marginBottom: 28,
  },
  retryButton: {
    backgroundColor: '#D4AF37',
    paddingHorizontal: 36,
    paddingVertical: 13,
    borderRadius: 10,
    marginBottom: 12,
  },
  retryButtonText: { color: '#FFF', fontSize: 15, fontWeight: '600' },
  goBackButton: { paddingHorizontal: 32, paddingVertical: 10 },
  goBackText: { color: '#888', fontSize: 14 },

  // Image
  imageSection: {
    backgroundColor: '#FFF',
    width: '100%',
    height: IMAGE_HEIGHT,
    justifyContent: 'center',
    alignItems: 'center',
  },
  mainImage: { width: '100%', height: '100%' },
  imageIndicator: {
    position: 'absolute',
    bottom: 12,
    right: 16,
    backgroundColor: 'rgba(0,0,0,0.5)',
    borderRadius: 12,
    paddingHorizontal: 10,
    paddingVertical: 4,
  },
  imageIndicatorText: { color: '#FFF', fontSize: 12, fontWeight: '500' },
  noImageContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#FAF8F3',
    width: '100%',
    overflow: 'hidden',
  },
  noImageBorder: {
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 32,
    paddingVertical: 24,
    borderWidth: 1.5,
    borderColor: 'rgba(212,175,55,0.2)',
    borderStyle: 'dashed',
    borderRadius: 16,
  },
  noImageIconCircle: {
    width: 88,
    height: 88,
    borderRadius: 44,
    backgroundColor: 'rgba(212,175,55,0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  noImageTitle: {
    fontSize: 16,
    fontWeight: '600',
    color: '#A39171',
    marginBottom: 6,
  },
  noImageSubtitle: {
    fontSize: 13,
    color: '#C4B89A',
    textAlign: 'center',
    maxWidth: 220,
  },
  noImageWatermark: {
    position: 'absolute',
    bottom: 16,
    fontSize: 11,
    fontWeight: '700',
    color: 'rgba(212,175,55,0.15)',
    letterSpacing: 4,
    textTransform: 'uppercase',
  },

  // Thumbnails
  thumbnailRow: {
    paddingHorizontal: 16,
    paddingVertical: 10,
    backgroundColor: '#FFF',
  },
  thumb: {
    width: 56,
    height: 56,
    borderRadius: 8,
    marginRight: 10,
    borderWidth: 2,
    borderColor: 'transparent',
    overflow: 'hidden',
  },
  thumbActive: { borderColor: '#D4AF37' },
  thumbImg: { width: '100%', height: '100%' },

  // Info Card
  infoCard: {
    backgroundColor: '#FFF',
    marginTop: 8,
    paddingHorizontal: 20,
    paddingVertical: 18,
  },
  priceRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 10,
  },
  price: { fontSize: 26, fontWeight: '800', color: '#D4AF37' },
  stockBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 10,
    paddingVertical: 5,
    borderRadius: 20,
  },
  stockIn: { backgroundColor: '#F0FDF4' },
  stockOut: { backgroundColor: '#FEF2F2' },
  stockText: { fontSize: 12, fontWeight: '600', marginLeft: 4 },
  stockTextIn: { color: '#16A34A' },
  stockTextOut: { color: '#DC2626' },
  productName: {
    fontSize: 20,
    fontWeight: '600',
    color: '#1A1A1A',
    lineHeight: 26,
    marginBottom: 10,
  },
  metaRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  categoryPill: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    backgroundColor: 'rgba(212,175,55,0.1)',
    paddingHorizontal: 10,
    paddingVertical: 5,
    borderRadius: 14,
  },
  categoryText: { fontSize: 12, color: '#8B6914', fontWeight: '500' },
  ratingPill: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
    backgroundColor: '#FFFBEB',
    paddingHorizontal: 10,
    paddingVertical: 5,
    borderRadius: 14,
  },
  ratingText: { fontSize: 12, fontWeight: '600', color: '#92400E' },
  reviewCount: { fontSize: 11, color: '#A3A3A3' },

  // Seller
  sellerCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    marginTop: 8,
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  sellerAvatar: {
    width: 44,
    height: 44,
    borderRadius: 22,
    backgroundColor: '#D4AF37',
    justifyContent: 'center',
    alignItems: 'center',
  },
  sellerInitial: { color: '#FFF', fontSize: 18, fontWeight: '700' },
  sellerInfo: { flex: 1, marginLeft: 12 },
  sellerName: {
    fontSize: 15,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 3,
  },
  sellerMeta: { flexDirection: 'row', alignItems: 'center', gap: 3 },
  sellerMetaText: { fontSize: 12, color: '#888', flex: 1 },
  sellerCta: { flexDirection: 'row', alignItems: 'center', gap: 2 },
  sellerCtaText: { fontSize: 13, color: '#D4AF37', fontWeight: '600' },

  // Sections
  section: {
    backgroundColor: '#FFF',
    marginTop: 8,
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  sectionTitle: {
    fontSize: 15,
    fontWeight: '700',
    color: '#1A1A1A',
    marginBottom: 10,
  },
  description: { fontSize: 14, color: '#555', lineHeight: 22 },

  // Specs
  specRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 10,
    borderBottomWidth: 1,
    borderBottomColor: '#F3F3F3',
  },
  specLabel: { fontSize: 13, color: '#888' },
  specValue: { fontSize: 13, color: '#1A1A1A', fontWeight: '500' },

  // Shipping
  shippingRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 8,
    gap: 8,
  },
  shippingLabel: { flex: 1, fontSize: 13, color: '#555' },
  shippingPrice: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },

  // Quantity
  quantityRow: { flexDirection: 'row', alignItems: 'center' },
  qtyBtn: {
    width: 38,
    height: 38,
    borderRadius: 19,
    borderWidth: 1.5,
    borderColor: '#D4AF37',
    justifyContent: 'center',
    alignItems: 'center',
  },
  qtyBtnDisabled: { borderColor: '#E5E5E5' },
  qtyValue: {
    fontSize: 18,
    fontWeight: '700',
    color: '#1A1A1A',
    marginHorizontal: 22,
  },
  qtyMax: { fontSize: 13, color: '#AAA', marginLeft: 12 },

  // Meta footer
  metaFooter: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 16,
    paddingHorizontal: 20,
    paddingVertical: 14,
    marginTop: 8,
  },
  metaItem: { flexDirection: 'row', alignItems: 'center', gap: 4 },
  metaText: { fontSize: 12, color: '#AAA' },

  // Bottom bar
  bottomBar: {
    position: 'absolute',
    bottom: 0,
    left: 0,
    right: 0,
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    paddingBottom: 28,
    backgroundColor: '#FFF',
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: -2 },
    shadowOpacity: 0.05,
    shadowRadius: 8,
    elevation: 8,
  },
  contactBtn: {
    width: 48,
    height: 48,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: '#D4AF37',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 10,
  },
  cartBtn: {
    flex: 1,
    flexDirection: 'row',
    height: 48,
    justifyContent: 'center',
    alignItems: 'center',
    gap: 6,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: '#D4AF37',
    marginRight: 10,
  },
  cartBtnText: { fontSize: 14, fontWeight: '600', color: '#D4AF37' },
  buyBtn: {
    flex: 1.2,
    height: 48,
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 12,
    backgroundColor: '#D4AF37',
  },
  buyBtnText: { fontSize: 14, fontWeight: '700', color: '#FFF' },
  buyBtnPrice: { fontSize: 11, color: 'rgba(255,255,255,0.8)', marginTop: 1 },
  oosBar: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
    paddingVertical: 14,
    backgroundColor: '#FEF2F2',
    borderRadius: 12,
  },
  oosText: { fontSize: 14, color: '#DC2626', fontWeight: '500' },
});
