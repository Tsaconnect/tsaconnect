import React from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  Image,
  StyleSheet,
  Dimensions,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const CARD_PADDING = 16;
const CARD_MARGIN = 16;
const GRID_GAP = 10;
const TILE_WIDTH = (SCREEN_WIDTH - CARD_MARGIN * 2 - CARD_PADDING * 2 - GRID_GAP) / 2;

interface SellerCardProps {
  seller: {
    sellerId: string;
    businessName?: string;
    name?: string;
    email: string;
    rating?: number;
    reviewCount?: number;
    location?: string;
    products?: any[];
    productCount?: number;
  };
  onPress: () => void;
  onProductPress: (productId: string, productData?: any) => void;
  searchQuery?: string;
}

export const SellerCard: React.FC<SellerCardProps> = ({
  seller,
  onPress,
  onProductPress,
  searchQuery,
}) => {
  const highlightText = (text: string) => {
    if (!searchQuery || !text) return text;
    const parts = text.split(new RegExp(`(${searchQuery})`, 'gi'));
    return (
      <Text>
        {parts.map((part, index) =>
          part.toLowerCase() === searchQuery.toLowerCase() ? (
            <Text key={index} style={styles.highlightedText}>{part}</Text>
          ) : (
            <Text key={index}>{part}</Text>
          )
        )}
      </Text>
    );
  };

  const formatPrice = (price: number) => `$${price.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;

  const getProductImage = (product: any): string | null => {
    if (product.image) return product.image;
    if (product.images?.length) {
      const withUrl = product.images.find((img: any) => img.url && img.url.startsWith('http'));
      if (withUrl) return withUrl.url;
    }
    return null;
  };

  const sellerDisplayName = seller.businessName || seller.name || seller.email?.split('@')[0] || 'Seller';
  const sellerInitial = sellerDisplayName.charAt(0).toUpperCase();
  const visibleProducts = (seller.products || []).slice(0, 4);
  const totalCount = seller.productCount || seller.products?.length || 0;
  const hasMoreProducts = totalCount > 4;
  const hasRating = seller.reviewCount != null && seller.reviewCount > 0;

  return (
    <View style={styles.container}>
      {/* ===== Product Grid (Hero) ===== */}
      <View style={styles.grid}>
        {visibleProducts.map((product: any, index: number) => {
          const imageUrl = getProductImage(product);
          return (
            <TouchableOpacity
              key={product._id || product.id || `p-${index}`}
              style={[styles.tile, visibleProducts.length === 1 && styles.tileFull]}
              onPress={() => onProductPress(product._id || product.id, product)}
              activeOpacity={0.7}
            >
              {/* Product Image */}
              <View style={styles.tileImageWrap}>
                {imageUrl ? (
                  <Image source={{ uri: imageUrl }} style={styles.tileImage} resizeMode="cover" />
                ) : (
                  <View style={styles.tilePlaceholder}>
                    <Ionicons name="image-outline" size={36} color="#C4B89A" />
                  </View>
                )}
                {/* Cart overlay */}
                <TouchableOpacity
                  style={styles.cartOverlay}
                  onPress={(e) => {
                    e.stopPropagation();
                    console.log('Add to cart:', product._id || product.id);
                  }}
                  hitSlop={{ top: 6, bottom: 6, left: 6, right: 6 }}
                >
                  <Ionicons name="cart-outline" size={14} color="#D4AF37" />
                </TouchableOpacity>
              </View>

              {/* Product Info */}
              <View style={styles.tileInfo}>
                <Text style={styles.tileName} numberOfLines={2}>
                  {searchQuery ? highlightText(product.name) : product.name}
                </Text>
                {product.condition && (
                  <View style={styles.conditionPill}>
                    <Text style={styles.conditionText}>{product.condition}</Text>
                  </View>
                )}
                <Text style={styles.tilePrice}>{formatPrice(product.price)}</Text>
              </View>
            </TouchableOpacity>
          );
        })}
      </View>

      {/* View All */}
      {hasMoreProducts && (
        <TouchableOpacity style={styles.viewAllRow} onPress={onPress} activeOpacity={0.7}>
          <Text style={styles.viewAllText}>View all {totalCount} products</Text>
          <Ionicons name="arrow-forward" size={14} color="#D4AF37" />
        </TouchableOpacity>
      )}

      {/* ===== Seller Attribution (Footer) ===== */}
      <TouchableOpacity style={styles.sellerFooter} onPress={onPress} activeOpacity={0.7}>
        <View style={styles.sellerAvatar}>
          <Text style={styles.sellerInitial}>{sellerInitial}</Text>
        </View>
        <View style={styles.sellerInfo}>
          <Text style={styles.sellerLabel} numberOfLines={1}>
            <Text style={styles.sellerPrefix}>Sold by </Text>
            {sellerDisplayName}
          </Text>
          {hasRating && (
            <View style={styles.ratingRow}>
              <Ionicons name="star" size={11} color="#F59E0B" />
              <Text style={styles.ratingText}>{seller.rating?.toFixed(1)}</Text>
              <Text style={styles.reviewText}>({seller.reviewCount} {seller.reviewCount === 1 ? 'review' : 'reviews'})</Text>
            </View>
          )}
        </View>
        <View style={styles.sellerCta}>
          <Text style={styles.sellerCtaCount}>{totalCount} {totalCount === 1 ? 'Product' : 'Products'}</Text>
          <Ionicons name="chevron-forward" size={14} color="#D4AF37" />
        </View>
      </TouchableOpacity>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    backgroundColor: '#FFF',
    marginHorizontal: CARD_MARGIN,
    marginVertical: 10,
    borderRadius: 14,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.06,
    shadowRadius: 8,
    elevation: 3,
    overflow: 'hidden',
  },

  // ── Product Grid ──
  grid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    padding: CARD_PADDING,
    gap: GRID_GAP,
  },
  tile: {
    width: TILE_WIDTH,
  },
  tileFull: {
    width: '100%',
  },
  tileImageWrap: {
    width: '100%',
    aspectRatio: 1,
    maxHeight: 200,
    borderRadius: 10,
    backgroundColor: '#F5F3EE',
    overflow: 'hidden',
  },
  tileImage: {
    width: '100%',
    height: '100%',
  },
  tilePlaceholder: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#F5F3EE',
  },
  cartOverlay: {
    position: 'absolute',
    bottom: 8,
    right: 8,
    width: 30,
    height: 30,
    borderRadius: 15,
    backgroundColor: 'rgba(255,255,255,0.92)',
    justifyContent: 'center',
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.1,
    shadowRadius: 3,
    elevation: 2,
  },
  tileInfo: {
    paddingTop: 8,
    paddingBottom: 4,
  },
  tileName: {
    fontSize: 13,
    fontWeight: '600',
    color: '#1A1A1A',
    lineHeight: 17,
    marginBottom: 4,
  },
  tilePrice: {
    fontSize: 16,
    fontWeight: '700',
    color: '#D4AF37',
  },
  conditionPill: {
    alignSelf: 'flex-start',
    backgroundColor: '#F0F0F0',
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 4,
    marginBottom: 4,
  },
  conditionText: {
    fontSize: 10,
    color: '#666',
  },
  highlightedText: {
    backgroundColor: '#FFEAA7',
    color: '#1A1A1A',
    fontWeight: '600',
  },

  // ── View All ──
  viewAllRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 12,
    marginHorizontal: CARD_PADDING,
    borderTopWidth: 1,
    borderTopColor: 'rgba(0,0,0,0.05)',
    gap: 4,
  },
  viewAllText: {
    fontSize: 13,
    color: '#D4AF37',
    fontWeight: '600',
  },

  // ── Seller Footer ──
  sellerFooter: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: CARD_PADDING,
    paddingVertical: 12,
    borderTopWidth: 1,
    borderTopColor: 'rgba(0,0,0,0.05)',
    backgroundColor: '#FAFAFA',
  },
  sellerAvatar: {
    width: 26,
    height: 26,
    borderRadius: 13,
    backgroundColor: '#D4AF37',
    justifyContent: 'center',
    alignItems: 'center',
  },
  sellerInitial: {
    color: '#FFF',
    fontSize: 12,
    fontWeight: '700',
  },
  sellerInfo: {
    flex: 1,
    marginLeft: 8,
  },
  sellerLabel: {
    fontSize: 13,
    fontWeight: '500',
    color: '#555',
  },
  sellerPrefix: {
    fontWeight: '400',
    color: '#999',
  },
  ratingRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 2,
    gap: 3,
  },
  ratingText: {
    fontSize: 11,
    fontWeight: '600',
    color: '#92400E',
  },
  reviewText: {
    fontSize: 11,
    color: '#AAA',
  },
  sellerCta: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 2,
  },
  sellerCtaCount: {
    fontSize: 12,
    color: '#D4AF37',
    fontWeight: '500',
  },
});

export default SellerCard;
