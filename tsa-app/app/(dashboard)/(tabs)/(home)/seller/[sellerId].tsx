// app/screens/seller/[sellerId].tsx
import React, { useCallback } from 'react';
import {
  View,
  FlatList,
  StyleSheet,
  Image,
  Text,
  TouchableOpacity,
  Dimensions,
  StatusBar,
  ActivityIndicator,
  RefreshControl,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Ionicons } from '@expo/vector-icons';
import { useSellerData } from '@/hooks/useSellerData';

const { width } = Dimensions.get('window');
const PRODUCT_WIDTH = (width - 48) / 2;

export default function SellerCatalogueScreen() {
  const {
    sellerId,
    sellerEmail,
    subcategoryId,
    categoryTitle
  } = useLocalSearchParams<{
    sellerId: string;
    sellerEmail: string;
    subcategoryId?: string;
    categoryTitle?: string;
  }>();

  const router = useRouter();

  const {
    sellerData,
    loading,
    error,
    refreshing,
    handleRefresh
  } = useSellerData({
    sellerEmail,
    sellerId,
    categoryTitle
  });

  const handleProductPress = useCallback((productId: string) => {
    router.push({
      pathname: '/product/[productId]',
      params: { productId }
    });
  }, [router]);

  const handleBackPress = useCallback(() => {
    if (router.canGoBack()) {
      router.back();
    } else {
      router.push('/marketplace');
    }
  }, [router]);

  const handleContactSeller = useCallback(() => {
    if (sellerData?.email) {
      router.push({
        pathname: '/messages/chat',
        params: {
          recipientEmail: sellerData.email,
          recipientName: sellerData.name
        }
      });
    }
  }, [sellerData, router]);

  const handleShareSeller = useCallback(() => {
    // Implement share functionality
    if (sellerData) {
      const shareMessage = `Check out ${sellerData.name}'s products on our marketplace!`;
      console.log('Share seller:', shareMessage);
      // You can implement native sharing here using expo-sharing or react-native-share
    }
  }, [sellerData]);

  const handleCallSeller = useCallback(() => {
    if (sellerData?.phoneNumber && sellerData.phoneNumber !== 'N/A' && sellerData.phoneNumber !== 'Contact for details') {
      const phoneNumber = sellerData.phoneNumber.replace(/\D/g, '');
      const url = `tel:${phoneNumber}`;
      console.log('Call seller:', url);
      // You can implement phone calling using Linking.openURL(url)
    }
  }, [sellerData]);

  const renderProduct = useCallback(({ item }: { item: any }) => (
    <TouchableOpacity
      style={styles.productCard}
      onPress={() => handleProductPress(item._id)}
      activeOpacity={0.9}
    >
      <View style={styles.imageContainer}>
        {item.images?.[0]?.url ? (
          <Image
            source={{ uri: item.images[0].url }}
            style={styles.productImage}
            resizeMode="cover"
          />
        ) : (
          <View style={[styles.productImage, styles.productImagePlaceholder]}>
            <Ionicons name="image-outline" size={32} color="#999" />
          </View>
        )}
        {item.isFeatured && (
          <View style={styles.featuredBadge}>
            <Text style={styles.featuredText}>FEATURED</Text>
          </View>
        )}
      </View>
      <View style={styles.productInfo}>
        <Text style={styles.productName} numberOfLines={2}>
          {item.name}
        </Text>
        <Text style={styles.productDescription} numberOfLines={2}>
          {item.description}
        </Text>
        <View style={styles.priceContainer}>
          <Text style={styles.productPrice}>
            ${item.price?.toFixed(2) || '0.00'}
          </Text>
          {item.stock !== undefined && (
            <View style={styles.stockContainer}>
              <Ionicons
                name="cube-outline"
                size={12}
                color={item.stock > 0 ? '#28A745' : '#DC3545'}
              />
              <Text style={[
                styles.stockText,
                { color: item.stock > 0 ? '#28A745' : '#DC3545' }
              ]}>
                {item.stock > 0 ? `${item.stock} in stock` : 'Out of stock'}
              </Text>
            </View>
          )}
        </View>
        {item.rating?.average > 0 && (
          <View style={styles.productRatingContainer}>
            <Ionicons name="star" size={12} color="#FFD700" />
            <Text style={styles.productRating}>
              {item.rating.average.toFixed(1)} ({item.rating.count || 0})
            </Text>
          </View>
        )}
      </View>
    </TouchableOpacity>
  ), [handleProductPress]);

  const renderHeader = useCallback(() => {
    if (!sellerData) return null;

    return (
      <View style={styles.sellerHeader}>
        <View style={styles.sellerProfile}>
          {sellerData.profilePhoto ? (
            <Image
              source={{ uri: sellerData.profilePhoto }}
              style={styles.sellerImage}
            />
          ) : (
            <View style={[styles.sellerImage, styles.sellerImagePlaceholder]}>
              <Ionicons name="person-outline" size={32} color="#666" />
            </View>
          )}
          <View style={styles.sellerInfo}>
            <View style={styles.nameContainer}>
              <Text style={styles.sellerName}>{sellerData.name}</Text>
              {sellerData.isVerified && (
                <Ionicons name="checkmark-circle" size={20} color="#4CAF50" />
              )}
            </View>
            <View style={styles.ratingContainer}>
              <Ionicons name="star" size={16} color="#FFD700" />
              <Text style={styles.ratingText}>
                {sellerData.rating.average.toFixed(1)}
              </Text>
              <Text style={styles.ratingCount}>
                ({sellerData.rating.count} reviews)
              </Text>
            </View>
          </View>
        </View>

        <View style={styles.actionButtons}>
          <TouchableOpacity
            style={styles.contactButton}
            onPress={handleContactSeller}
          >
            <Ionicons name="chatbubble-outline" size={20} color="#FFFFFF" />
            <Text style={styles.contactButtonText}>Message</Text>
          </TouchableOpacity>

          {sellerData.phoneNumber &&
            sellerData.phoneNumber !== 'N/A' &&
            sellerData.phoneNumber !== 'Contact for details' && (
              <TouchableOpacity
                style={styles.callButton}
                onPress={handleCallSeller}
              >
                <Ionicons name="call-outline" size={20} color="#FFFFFF" />
                <Text style={styles.callButtonText}>Call</Text>
              </TouchableOpacity>
            )}
        </View>

        <View style={styles.sellerDetails}>
          <View style={styles.detailItem}>
            <Ionicons name="location-outline" size={18} color="#666" />
            <Text style={styles.detailText}>{sellerData.location}</Text>
          </View>

          <View style={styles.detailItem}>
            <Ionicons name="mail-outline" size={18} color="#666" />
            <Text style={styles.detailText}>{sellerData.email}</Text>
          </View>

          {sellerData.phoneNumber && (
            <View style={styles.detailItem}>
              <Ionicons name="call-outline" size={18} color="#666" />
              <Text style={styles.detailText}>{sellerData.phoneNumber}</Text>
            </View>
          )}

          <View style={styles.detailItem}>
            <Ionicons name="calendar-outline" size={18} color="#666" />
            <Text style={styles.detailText}>
              Joined {new Date(sellerData.joinedDate).toLocaleDateString()}
            </Text>
          </View>
        </View>

        {sellerData.description && (
          <View style={styles.descriptionContainer}>
            <Text style={styles.descriptionTitle}>About This Seller</Text>
            <Text style={styles.descriptionText}>{sellerData.description}</Text>
          </View>
        )}

        <View style={styles.statsContainer}>
          <View style={styles.statCard}>
            <Text style={styles.statNumber}>{sellerData.productCount}</Text>
            <Text style={styles.statLabel}>Products</Text>
          </View>
          <View style={styles.statCard}>
            <Text style={styles.statNumber}>
              {sellerData.totalSales?.toLocaleString() || '0'}
            </Text>
            <Text style={styles.statLabel}>Sales</Text>
          </View>
          <View style={styles.statCard}>
            <Text style={styles.statNumber}>
              {sellerData.rating.average.toFixed(1)}
            </Text>
            <Text style={styles.statLabel}>Rating</Text>
          </View>
          <View style={styles.statCard}>
            <Text style={styles.statNumber}>
              {Math.floor(sellerData.rating.count)}
            </Text>
            <Text style={styles.statLabel}>Reviews</Text>
          </View>
        </View>

        <View style={styles.catalogueHeader}>
          <Text style={styles.catalogueTitle}>Product Catalogue</Text>
          <Text style={styles.productCount}>
            {sellerData.products.length} {sellerData.products.length === 1 ? 'product' : 'products'}
          </Text>
        </View>
      </View>
    );
  }, [sellerData, handleContactSeller, handleCallSeller]);

  const renderEmptyState = useCallback(() => (
    <View style={styles.emptyState}>
      <Ionicons name="bag-outline" size={64} color="#999" />
      <Text style={styles.emptyStateTitle}>No Products Yet</Text>
      <Text style={styles.emptyStateText}>
        {sellerData?.name} hasn't listed any products yet.
      </Text>
      <TouchableOpacity
        style={styles.browseButton}
        onPress={() => router.push('/marketplace')}
      >
        <Text style={styles.browseButtonText}>Browse Other Sellers</Text>
      </TouchableOpacity>
    </View>
  ), [sellerData, router]);

  if (loading && !sellerData) {
    return (
      <SafeAreaView style={[styles.safeArea, styles.centered]}>
        <Stack.Screen
          options={{
            title: 'Loading...',
            headerShown: true,
            headerBackTitle: 'Back',
            headerTintColor: '#D4AF37',
            headerStyle: {
              backgroundColor: '#FFFFFF',
            },
          }}
        />
        <ActivityIndicator size="large" color="#D4AF37" />
        <Text style={styles.loadingText}>Loading seller information...</Text>
      </SafeAreaView>
    );
  }

  if (error && !sellerData) {
    return (
      <SafeAreaView style={[styles.safeArea, styles.centered]}>
        <Stack.Screen
          options={{
            title: 'Error',
            headerShown: true,
          }}
        />
        <Ionicons name="alert-circle-outline" size={64} color="#DC3545" />
        <Text style={styles.errorText}>{error}</Text>
        <TouchableOpacity
          style={styles.retryButton}
          onPress={handleRefresh}
        >
          <Text style={styles.retryButtonText}>Retry</Text>
        </TouchableOpacity>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.safeArea} edges={['top', 'left', 'right']}>
      <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

      <Stack.Screen
        options={{
          title: sellerData?.name || 'Seller',
          headerShown: true,
          headerBackTitle: 'Back',
          headerTintColor: '#D4AF37',
          headerStyle: {
            backgroundColor: '#FFFFFF',
          },
          headerTitleStyle: {
            fontWeight: '600',
            color: '#1A1A1A',
            fontSize: 16,
          },
          headerRight: () => (
            <TouchableOpacity
              style={styles.headerAction}
              onPress={handleShareSeller}
            >
              <Ionicons name="share-outline" size={22} color="#D4AF37" />
            </TouchableOpacity>
          ),
        }}
      />

      <FlatList
        data={sellerData?.products || []}
        numColumns={2}
        keyExtractor={(item) => item._id}
        renderItem={renderProduct}
        ListHeaderComponent={renderHeader}
        ListEmptyComponent={renderEmptyState}
        columnWrapperStyle={styles.columnWrapper}
        contentContainerStyle={[
          styles.listContent,
          (!sellerData?.products || sellerData.products.length === 0) && styles.listContentEmpty
        ]}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={handleRefresh}
            tintColor="#D4AF37"
            colors={['#D4AF37']}
          />
        }
        ListFooterComponent={<View style={styles.footer} />}
        initialNumToRender={8}
        maxToRenderPerBatch={10}
        windowSize={10}
      />
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
    backgroundColor: '#F8F9FA',
  },
  centered: {
    justifyContent: 'center',
    alignItems: 'center',
    padding: 20,
  },
  loadingText: {
    marginTop: 16,
    fontSize: 16,
    color: '#666',
    textAlign: 'center',
  },
  errorText: {
    marginTop: 16,
    fontSize: 16,
    color: '#DC3545',
    textAlign: 'center',
    marginBottom: 20,
    paddingHorizontal: 20,
  },
  retryButton: {
    backgroundColor: '#D4AF37',
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 8,
  },
  retryButtonText: {
    color: '#FFFFFF',
    fontSize: 16,
    fontWeight: '600',
  },
  sellerHeader: {
    backgroundColor: '#FFFFFF',
    padding: 20,
    marginBottom: 16,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(0, 0, 0, 0.05)',
  },
  sellerProfile: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 20,
  },
  sellerImage: {
    width: 80,
    height: 80,
    borderRadius: 40,
    borderWidth: 3,
    borderColor: '#D4AF37',
  },
  sellerImagePlaceholder: {
    backgroundColor: '#F5F5F5',
    justifyContent: 'center',
    alignItems: 'center',
  },
  sellerInfo: {
    flex: 1,
    marginLeft: 16,
  },
  nameContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 6,
  },
  sellerName: {
    fontSize: 22,
    fontWeight: '700',
    color: '#1A1A1A',
    marginRight: 8,
    flexShrink: 1,
  },
  ratingContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    flexWrap: 'wrap',
  },
  ratingText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#B8860B',
    marginLeft: 4,
    marginRight: 8,
  },
  ratingCount: {
    fontSize: 14,
    color: '#999',
  },
  actionButtons: {
    flexDirection: 'row',
    gap: 12,
    marginBottom: 20,
  },
  contactButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#D4AF37',
    paddingVertical: 12,
    borderRadius: 10,
  },
  contactButtonText: {
    color: '#FFFFFF',
    fontSize: 16,
    fontWeight: '600',
    marginLeft: 8,
  },
  callButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#28A745',
    paddingVertical: 12,
    borderRadius: 10,
  },
  callButtonText: {
    color: '#FFFFFF',
    fontSize: 16,
    fontWeight: '600',
    marginLeft: 8,
  },
  sellerDetails: {
    backgroundColor: 'rgba(0, 0, 0, 0.02)',
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
  },
  detailItem: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  detailText: {
    fontSize: 14,
    color: '#666',
    marginLeft: 12,
    flex: 1,
    flexWrap: 'wrap',
  },
  descriptionContainer: {
    marginBottom: 20,
  },
  descriptionTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 8,
  },
  descriptionText: {
    fontSize: 14,
    color: '#666',
    lineHeight: 22,
  },
  statsContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 24,
  },
  statCard: {
    alignItems: 'center',
    padding: 12,
    backgroundColor: 'rgba(212, 175, 55, 0.08)',
    borderRadius: 12,
    flex: 1,
    marginHorizontal: 4,
    minWidth: 70,
  },
  statNumber: {
    fontSize: 20,
    fontWeight: '700',
    color: '#D4AF37',
    marginBottom: 4,
  },
  statLabel: {
    fontSize: 12,
    color: '#666',
    fontWeight: '500',
    textAlign: 'center',
  },
  catalogueHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 16,
  },
  catalogueTitle: {
    fontSize: 20,
    fontWeight: '600',
    color: '#1A1A1A',
  },
  productCount: {
    fontSize: 14,
    color: '#666',
    backgroundColor: 'rgba(212, 175, 55, 0.1)',
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 12,
  },
  listContent: {
    paddingBottom: 40,
  },
  listContentEmpty: {
    flexGrow: 1,
  },
  columnWrapper: {
    justifyContent: 'space-between',
    paddingHorizontal: 16,
    marginBottom: 16,
  },
  productCard: {
    width: PRODUCT_WIDTH,
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    overflow: 'hidden',
    borderWidth: 1,
    borderColor: 'rgba(0, 0, 0, 0.08)',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 6,
    elevation: 3,
  },
  imageContainer: {
    position: 'relative',
  },
  productImage: {
    width: '100%',
    height: PRODUCT_WIDTH,
  },
  productImagePlaceholder: {
    backgroundColor: '#F5F5F5',
    justifyContent: 'center',
    alignItems: 'center',
  },
  featuredBadge: {
    position: 'absolute',
    top: 8,
    right: 8,
    backgroundColor: '#D4AF37',
    borderRadius: 4,
    paddingHorizontal: 6,
    paddingVertical: 2,
  },
  featuredText: {
    fontSize: 9,
    color: '#FFFFFF',
    fontWeight: 'bold',
  },
  productInfo: {
    padding: 12,
  },
  productName: {
    fontSize: 14,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 6,
    lineHeight: 18,
    height: 36,
  },
  productDescription: {
    fontSize: 12,
    color: '#666',
    marginBottom: 8,
    lineHeight: 16,
    height: 32,
  },
  priceContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 6,
  },
  productPrice: {
    fontSize: 16,
    fontWeight: '700',
    color: '#D4AF37',
  },
  stockContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  stockText: {
    fontSize: 11,
    marginLeft: 4,
  },
  productRatingContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  productRating: {
    fontSize: 11,
    color: '#666',
    marginLeft: 4,
  },
  footer: {
    height: 40,
  },
  headerAction: {
    marginRight: 16,
    padding: 4,
  },
  emptyState: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingVertical: 60,
    paddingHorizontal: 20,
  },
  emptyStateTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: '#333',
    marginTop: 16,
    marginBottom: 8,
  },
  emptyStateText: {
    fontSize: 14,
    color: '#666',
    textAlign: 'center',
    marginBottom: 24,
  },
  browseButton: {
    backgroundColor: '#D4AF37',
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 8,
  },
  browseButtonText: {
    color: '#FFFFFF',
    fontSize: 16,
    fontWeight: '600',
  },
});