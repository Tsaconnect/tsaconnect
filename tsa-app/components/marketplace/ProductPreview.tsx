import React, { memo } from 'react';
import {
  View,
  Text,
  Image,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  Dimensions
} from 'react-native';
import { Product } from '@/types/marketplace';
import { useRouter } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';

interface ProductPreviewProps {
  products: Product[];
  searchQuery?: string;
}

const { width } = Dimensions.get('window');
const PRODUCT_PREVIEW_WIDTH = (width - 80) / 3;

export const ProductPreview = memo(({ products, searchQuery }: ProductPreviewProps) => {
  const router = useRouter();
  
  if (products.length === 0) {
    return (
      <View style={styles.emptyContainer}>
        <Ionicons name="cube-outline" size={32} color="#D4AF37" />
        <Text style={styles.emptyText}>No products available</Text>
      </View>
    );
  }

  const handleProductPress = (productId: string) => {
    router.push(`/product/${productId}`);
  };

  const highlightText = (text: string, highlight?: string) => {
    if (!highlight || !text.toLowerCase().includes(highlight.toLowerCase())) {
      return <Text style={styles.productName} numberOfLines={1}>{text}</Text>;
    }
    
    const parts = text.split(new RegExp(`(${highlight})`, 'gi'));
    return (
      <Text style={styles.productName} numberOfLines={1}>
        {parts.map((part, i) => 
          part.toLowerCase() === highlight.toLowerCase() ? (
            <Text key={i} style={styles.highlightedText}>
              {part}
            </Text>
          ) : (
            <Text key={i}>{part}</Text>
          )
        )}
      </Text>
    );
  };

  return (
    <View style={styles.container}>
      <View style={styles.sectionHeader}>
        <Text style={styles.sectionTitle}>Products</Text>
        <Text style={styles.productCount}>{products.length} items</Text>
      </View>
      
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={styles.scrollContent}
      >
        {products.slice(0, 6).map((product) => (
          <TouchableOpacity
            key={product.productId}
            style={styles.productCard}
            onPress={() => handleProductPress(product.productId)}
            activeOpacity={0.8}
          >
            <View style={styles.imageContainer}>
              <Image
                source={{ uri: product.image }}
                style={styles.productImage}
                resizeMode="cover"
              />
              {product.isFeatured && (
                <View style={styles.featuredBadge}>
                  <Text style={styles.featuredText}>🔥</Text>
                </View>
              )}
              {product.stock && product.stock < 10 && (
                <View style={styles.lowStockBadge}>
                  <Text style={styles.lowStockText}>Low Stock</Text>
                </View>
              )}
            </View>
            <View style={styles.productInfo}>
              {highlightText(product.name, searchQuery)}
              <View style={styles.priceContainer}>
                <Text style={styles.productPrice}>
                  ${product.price.toFixed(2)}
                </Text>
                {product.stock && (
                  <Text style={styles.stockText}>
                    {product.stock} left
                  </Text>
                )}
              </View>
            </View>
          </TouchableOpacity>
        ))}
        
        {products.length > 6 && (
          <TouchableOpacity
            style={styles.moreContainer}
            onPress={() => {
              // Navigate to full product list
              const sellerId = products[0]?.sellerId;
              if (sellerId) {
                router.push(`/seller/${sellerId}`);
              }
            }}
          >
            <View style={styles.moreContent}>
              <Ionicons name="grid-outline" size={24} color="#D4AF37" />
              <Text style={styles.moreText}>
                View all{'\n'}
                <Text style={styles.moreCount}>+{products.length - 6} more</Text>
              </Text>
            </View>
          </TouchableOpacity>
        )}
      </ScrollView>
    </View>
  );
});

const styles = StyleSheet.create({
  container: {
    marginTop: 8,
  },
  sectionHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
    paddingHorizontal: 4,
  },
  sectionTitle: {
    fontSize: 16,
    fontWeight: '600',
    color: '#1A1A1A',
  },
  productCount: {
    fontSize: 12,
    color: '#666',
    backgroundColor: 'rgba(212, 175, 55, 0.1)',
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: 12,
  },
  scrollContent: {
    paddingRight: 16,
  },
  productCard: {
    width: PRODUCT_PREVIEW_WIDTH,
    marginRight: 12,
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    overflow: 'hidden',
    borderWidth: 1,
    borderColor: 'rgba(0, 0, 0, 0.08)',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  imageContainer: {
    position: 'relative',
  },
  productImage: {
    width: '100%',
    height: PRODUCT_PREVIEW_WIDTH,
    backgroundColor: '#F5F5F5',
  },
  featuredBadge: {
    position: 'absolute',
    top: 8,
    left: 8,
    backgroundColor: 'rgba(255, 69, 0, 0.9)',
    borderRadius: 4,
    paddingHorizontal: 6,
    paddingVertical: 2,
  },
  featuredText: {
    fontSize: 10,
    color: '#FFFFFF',
    fontWeight: 'bold',
  },
  lowStockBadge: {
    position: 'absolute',
    bottom: 8,
    right: 8,
    backgroundColor: 'rgba(255, 215, 0, 0.9)',
    borderRadius: 4,
    paddingHorizontal: 6,
    paddingVertical: 2,
  },
  lowStockText: {
    fontSize: 9,
    color: '#1A1A1A',
    fontWeight: 'bold',
  },
  productInfo: {
    padding: 10,
  },
  productName: {
    fontSize: 12,
    fontWeight: '500',
    color: '#333',
    marginBottom: 6,
    lineHeight: 16,
  },
  highlightedText: {
    backgroundColor: 'rgba(212, 175, 55, 0.3)',
    borderRadius: 2,
    paddingHorizontal: 1,
    color: '#1A1A1A',
    fontWeight: '700',
  },
  priceContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  productPrice: {
    fontSize: 14,
    fontWeight: '700',
    color: '#D4AF37',
  },
  stockText: {
    fontSize: 10,
    color: '#666',
    fontStyle: 'italic',
  },
  emptyContainer: {
    padding: 20,
    alignItems: 'center',
    backgroundColor: 'rgba(212, 175, 55, 0.05)',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: 'rgba(212, 175, 55, 0.1)',
    borderStyle: 'dashed',
  },
  emptyText: {
    fontSize: 14,
    color: '#999',
    marginTop: 8,
  },
  moreContainer: {
    width: PRODUCT_PREVIEW_WIDTH,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: 'rgba(212, 175, 55, 0.05)',
    borderRadius: 12,
    borderWidth: 2,
    borderColor: 'rgba(212, 175, 55, 0.2)',
    borderStyle: 'dashed',
  },
  moreContent: {
    alignItems: 'center',
    padding: 8,
  },
  moreText: {
    fontSize: 11,
    color: '#666',
    textAlign: 'center',
    marginTop: 6,
    lineHeight: 14,
  },
  moreCount: {
    fontSize: 10,
    fontWeight: '700',
    color: '#D4AF37',
  },
});