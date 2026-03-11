import React from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  Image,
  StyleSheet,
  Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';

interface Product {
  id: string;
  name: string;
  price: number;
  image?: string;
  condition?: string;
}

interface SellerCardProps {
  seller: {
    sellerId: string;
    businessName?: string;
    email: string;
    rating?: number;
    reviewCount?: number;
    location?: string;
    products?: Product[];
    productCount?: number;
  };
  onPress: () => void;
  onProductPress: (productId: string) => void;
  searchQuery?: string;
}

export const SellerCard: React.FC<SellerCardProps> = ({
  seller,
  onPress,
  onProductPress,
  searchQuery,
}) => {
  console.log('SellerCard seller:', seller);
  const highlightText = (text: string) => {
    if (!searchQuery || !text) return text;

    // Simple text highlighting - you can enhance this based on your needs
    const parts = text.split(new RegExp(`(${searchQuery})`, 'gi'));
    return (
      <Text>
        {parts.map((part, index) =>
          part.toLowerCase() === searchQuery.toLowerCase() ? (
            <Text key={index} style={styles.highlightedText}>
              {part}
            </Text>
          ) : (
            <Text key={index}>{part}</Text>
          )
        )}
      </Text>
    );
  };

  const formatPrice = (price: number) => {
    return `₦${price.toLocaleString()}`;
  };

  return (
    <View style={styles.container}>
      {/* Seller Header - Taps to view all seller products */}
      <TouchableOpacity
        style={styles.sellerHeader}
        onPress={onPress}
        activeOpacity={0.7}
      >
        <View style={styles.sellerInfo}>
          <View style={styles.avatarContainer}>
            <Text style={styles.avatarText}>
              {seller.businessName?.charAt(0).toUpperCase() ||
                seller.email?.charAt(0).toUpperCase() ||
                'S'}
            </Text>
          </View>
          <View style={styles.sellerDetails}>
            <Text style={styles.sellerName} numberOfLines={1}>
              {seller.businessName || 'Unknown Seller'}
            </Text>
            <View style={styles.ratingContainer}>
              <Ionicons name="star" size={14} color="#FFC107" />
              <Text style={styles.rating}>
                {/* seller.rating?.toFixed(1) || */ '4.0'}
              </Text>
              <Text style={styles.reviews}>
                ({seller.reviewCount || 0} {seller.reviewCount === 1 ? 'review' : 'reviews'})
              </Text>
            </View>
            {seller.location && (
              <View style={styles.locationContainer}>
                <Ionicons name="location-outline" size={12} color="#666" />
                <Text style={styles.location} numberOfLines={1}>
                  {seller.location}
                </Text>
              </View>
            )}
          </View>
        </View>
        <View style={styles.productCountBadge}>
          <Text style={styles.productCountText}>
            {seller.productCount || 0} {seller.productCount === 1 ? 'Product' : 'Products'}
          </Text>
          <Ionicons name="chevron-forward" size={16} color="#D4AF37" />
        </View>
      </TouchableOpacity>

      {/* Products List - Each product is tappable with unique key */}
      {seller.products && seller.products.length > 0 && (
        <View style={styles.productsContainer}>
          {seller.products.map((product: any, index) => (
            <TouchableOpacity
              key={product.id || `product-${seller.sellerId}-${index}`}
              style={styles.productCard}
              onPress={() => onProductPress(product._id)}
              activeOpacity={0.7}
            >
              <View style={styles.productImageContainer}>
                {product.image ? (
                  <Image
                    source={{ uri: product.image }}
                    style={styles.productImage}
                    resizeMode="cover"
                  />
                ) : (
                  <View style={styles.productPlaceholder}>
                    <Ionicons name="image-outline" size={24} color="#CCC" />
                  </View>
                )}
              </View>

              <View style={styles.productInfo}>
                <Text style={styles.productName} numberOfLines={2}>
                  {searchQuery ? highlightText(product.name) : product.name}
                </Text>

                {product.condition && (
                  <View style={styles.conditionBadge}>
                    <Text style={styles.productCondition}>
                      {product.condition}
                    </Text>
                  </View>
                )}

                <Text style={styles.productPrice}>
                  {formatPrice(product.price)}
                </Text>
              </View>

              <TouchableOpacity
                style={styles.addToCartButton}
                onPress={(e) => {
                  e.stopPropagation();
                  // Handle add to cart
                  console.log('Add to cart:', {
                    productId: product.id,
                    sellerId: seller.sellerId,
                    productName: product.name,
                    price: product.price,
                  });
                  // You can trigger a callback here if needed
                }}
              >
                <Ionicons name="cart-outline" size={18} color="#D4AF37" />
              </TouchableOpacity>
            </TouchableOpacity>
          ))}

          {/* View All Products Link - Show if more than 3 products */}
          {seller.productCount && seller.productCount > 3 && (
            <TouchableOpacity
              style={styles.viewAllLink}
              onPress={onPress}
              activeOpacity={0.7}
            >
              <Text style={styles.viewAllText}>
                View all {seller.productCount} products
              </Text>
              <Ionicons name="arrow-forward" size={14} color="#D4AF37" />
            </TouchableOpacity>
          )}
        </View>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 16,
    marginVertical: 8,
    borderRadius: 12,
    elevation: 2,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    ...Platform.select({
      android: {
        elevation: 3,
      },
    }),
  },
  sellerHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 16,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(0,0,0,0.05)',
  },
  sellerInfo: {
    flexDirection: 'row',
    flex: 1,
    marginRight: 12,
  },
  avatarContainer: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: '#D4AF37',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  avatarText: {
    color: '#FFFFFF',
    fontSize: 18,
    fontWeight: '600',
  },
  sellerDetails: {
    flex: 1,
    justifyContent: 'center',
  },
  sellerName: {
    fontSize: 16,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 4,
  },
  ratingContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 4,
  },
  rating: {
    fontSize: 13,
    fontWeight: '600',
    color: '#1A1A1A',
    marginLeft: 4,
  },
  reviews: {
    fontSize: 12,
    color: '#666',
    marginLeft: 4,
  },
  locationContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  location: {
    fontSize: 12,
    color: '#666',
    marginLeft: 4,
    flex: 1,
  },
  productCountBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(212,175,55,0.1)',
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 16,
  },
  productCountText: {
    fontSize: 12,
    color: '#D4AF37',
    fontWeight: '600',
    marginRight: 4,
  },
  productsContainer: {
    paddingHorizontal: 16,
    paddingTop: 8,
    paddingBottom: 16,
  },
  productCard: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(0,0,0,0.05)',
  },
  productImageContainer: {
    width: 60,
    height: 60,
    borderRadius: 8,
    backgroundColor: '#F8F9FA',
    overflow: 'hidden',
    marginRight: 12,
  },
  productImage: {
    width: '100%',
    height: '100%',
  },
  productPlaceholder: {
    width: '100%',
    height: '100%',
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#F0F0F0',
  },
  productInfo: {
    flex: 1,
    marginRight: 8,
  },
  productName: {
    fontSize: 14,
    fontWeight: '500',
    color: '#1A1A1A',
    marginBottom: 4,
  },
  highlightedText: {
    backgroundColor: '#FFEAA7',
    color: '#1A1A1A',
    fontWeight: '600',
  },
  conditionBadge: {
    backgroundColor: 'rgba(0,0,0,0.05)',
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: 4,
    alignSelf: 'flex-start',
    marginBottom: 4,
  },
  productCondition: {
    fontSize: 11,
    color: '#666',
  },
  productPrice: {
    fontSize: 15,
    fontWeight: '700',
    color: '#D4AF37',
  },
  addToCartButton: {
    padding: 8,
    borderRadius: 8,
    backgroundColor: 'rgba(212,175,55,0.1)',
    marginLeft: 4,
  },
  viewAllLink: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingTop: 16,
    marginTop: 8,
    borderTopWidth: 1,
    borderTopColor: 'rgba(0,0,0,0.05)',
  },
  viewAllText: {
    fontSize: 14,
    color: '#D4AF37',
    fontWeight: '600',
    marginRight: 4,
  },
});

export default SellerCard;