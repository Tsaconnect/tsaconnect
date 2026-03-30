import React, { useState, useCallback, useMemo } from 'react';
import {
  View,
  FlatList,
  StyleSheet,
  RefreshControl,
  StatusBar,
  Platform,
  Text,
  Pressable,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { SearchBar } from '@/components/common/SearchBar';
import { EmptyState } from '@/components/common/EmptyState';
import { LoadingState } from '@/components/common/LoadingState';
import { ErrorState } from '@/components/common/ErrorState';
import { useSubcategoryProducts } from '@/hooks/useSubcategoryProducts';
import { Ionicons } from '@expo/vector-icons';
import { SellerCard } from '@/components/marketplace/SellerCard';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';


const SORT_OPTIONS = [
  'recent',
  'popular',
  'price_low',
  'price_high',
  'rating',
] as const;

type SortOption = typeof SORT_OPTIONS[number];

/* ---------------------------------- */
/* Component */
/* ---------------------------------- */

export default function SubcategorySellersScreen() {

  const { subcategoryId, categoryId, categoryTitle, subcategoryName, categoryType, userRole } =
    useLocalSearchParams<{
      subcategoryId: string;
      categoryId?: string;
      categoryTitle?: string;
      subcategoryName?: string;
      categoryType?: string;
      userRole?: string;
    }>();

  const isService = categoryType === 'Service';
  const isMerchant = userRole === 'merchant' || userRole === 'admin' || userRole === 'super_admin';

  const router = useRouter();

  const [searchQuery, setSearchQuery] = useState('');
  const [sortOption, setSortOption] = useState<SortOption>('recent');

  const {
    sellers,
    loading,
    error,
    refreshing,
    categoryInfo,
    totalProducts,
    showingProducts,
    handleRefresh,
  } = useSubcategoryProducts({
    subcategoryId,
    categoryId,
    searchQuery,
    sort: sortOption,
    limit: 20,
  });


  const baseHeaderOptions = useMemo(
    () => ({
      headerShown: true,
      headerBackTitle: 'Back',
      headerTintColor: '#D4AF37',
      headerStyle: { backgroundColor: '#FFFFFF' },
      headerTitleStyle: {
        fontWeight: '600' as const,
        color: '#1A1A1A',
      },
    }),
    []
  );



  const handleSearch = useCallback((query: string) => {
    setSearchQuery(query);
  }, []);

  const handleSellerPress = useCallback(
    (sellerId: string, sellerEmail: string) => {
      router.push(`/seller/${sellerId}?subcategoryId=${subcategoryId}&sellerEmail=${encodeURIComponent(sellerEmail)}`);
    },
    [router, subcategoryId]
  );
  const handleProductPress = useCallback(
    (productId: string, productData?: any) => {
      if (productData) {
        router.push(`/product/${productId}?productData=${encodeURIComponent(JSON.stringify(productData))}`);
      } else {
        router.push(`/product/${productId}`);
      }
    },
    [router]
  );

  const handleBackPress = useCallback(() => {
    if (router.canGoBack()) {
      router.back();
    } else {
      router.push('/digital');
    }
  }, [router]);

  const renderHeader = useCallback(() => (
    <View style={styles.header}>

      {/* Category Info */}
      <View style={styles.categoryHeader}>
        <View style={styles.categoryIconContainer}>
          <Icon
            name={categoryInfo?.icon ?? 'category'}
            size={32}
            color="#D4AF37"
          />
        </View>

        <View style={styles.categoryInfo}>
          <Text style={styles.categoryTitle}>
            {subcategoryName || categoryInfo?.title || 'Products'}
          </Text>

          <Text style={styles.categoryDescription}>
            {categoryInfo?.description || (isService ? 'Browse services in this category' : 'Browse products in this category')}
          </Text>

          {totalProducts > 0 && (
            <Text style={styles.productCount}>
              {totalProducts} {isService ? 'services' : 'products'} available
            </Text>
          )}
        </View>
      </View>

      {/* Search */}
      <SearchBar
        placeholder="Search sellers, products, or emails..."
        onSearch={handleSearch}
        initialValue={searchQuery}
        debounceDelay={400}
      /* style={styles.searchBar} */
      />

      {/* Sort */}
      <View style={styles.sortContainer}>
        <Text style={styles.sortLabel}>Sort by:</Text>

        <View style={styles.sortOptions}>
          {SORT_OPTIONS.map(option => (
            <Pressable
              key={option}
              style={[
                styles.sortOption,
                sortOption === option && styles.activeSortOption,
              ]}
              onPress={() => setSortOption(option)}
            >
              <Text
                style={[
                  styles.sortOptionText,
                  sortOption === option && styles.activeSortOptionText,
                ]}
              >
                {option === 'recent' && 'Recent'}
                {option === 'popular' && 'Popular'}
                {option === 'price_low' && 'Price: Low to High'}
                {option === 'price_high' && 'Price: High to Low'}
                {option === 'rating' && 'Rating'}
              </Text>
            </Pressable>
          ))}
        </View>
      </View>

      {/* Results */}
      {!loading && (
        <View style={styles.resultsInfo}>
          <Text style={styles.resultsText}>
            Showing {showingProducts} of {totalProducts} {isService ? 'services' : 'products'}
            {searchQuery ? ` for "${searchQuery}"` : ''}
          </Text>
        </View>
      )}
    </View>
  ), [categoryInfo, subcategoryName, searchQuery, sortOption, totalProducts, showingProducts, loading]);

  /* ---------------------------------- */
  /* Empty State */
  /* ---------------------------------- */

  const renderEmptyState = () => {
    if (searchQuery) {
      return (
        <EmptyState
          title="No Results Found"
          message={`No sellers or products match "${searchQuery}"`}
          icon="search-outline"
          action={
            <Pressable
              style={styles.clearSearchButton}
              onPress={() => setSearchQuery('')}
            >
              <Text style={styles.clearSearchText}>Clear search</Text>
            </Pressable>
          }
        />
      );
    }

    return (
      <EmptyState
        title={isService ? "No Services Available" : "No Products Available"}
        message={isService ? "There are no services in this category yet." : "There are no products in this category yet."}
        icon="storefront-outline"
        action={
          isMerchant ? (
            <Pressable
              style={styles.becomeSellerButton}
              onPress={() => router.push(isService ? '/serviceaction?index=1' : '/merchants/inventory/add')}
            >
              <Text style={styles.becomeSellerText}>{isService ? "List a Service" : "List a Product"}</Text>
            </Pressable>
          ) : (
            <Pressable
              style={styles.becomeSellerButton}
              onPress={() => router.push('/merchants/merchant-request')}
            >
              <Text style={styles.becomeSellerText}>Become a Merchant</Text>
            </Pressable>
          )
        }
      />
    );
  };

  /* ---------------------------------- */
  /* Loading */
  /* ---------------------------------- */

  if (loading && !refreshing && sellers.length === 0) {
    return (
      <SafeAreaView style={styles.safeArea}>
        <Stack.Screen
          options={{
            ...baseHeaderOptions,
            title: subcategoryName || categoryTitle || 'Loading...',
          }}
        />
        <LoadingState /* message="Loading products..." */ />
      </SafeAreaView>
    );
  }

  /* ---------------------------------- */
  /* Error */
  /* ---------------------------------- */

  if (error) {
    return (
      <SafeAreaView style={styles.safeArea}>
        <Stack.Screen
          options={{
            ...baseHeaderOptions,
            title: subcategoryName || categoryTitle || 'Error',
          }}
        />
        <ErrorState
          message={error || 'Failed to load sellers. Please try again.'}
          onRetry={handleRefresh}
        />
      </SafeAreaView>
    );
  }

  /* ---------------------------------- */
  /* Main Render */
  /* ---------------------------------- */

  return (
    <SafeAreaView style={styles.safeArea}>
      <StatusBar barStyle="dark-content" backgroundColor="#FFFFFF" />

      <Stack.Screen
        options={{
          ...baseHeaderOptions,
          title: subcategoryName || categoryTitle || (isService ? 'Services' : 'Products'),
          headerLeft: () => (
            <Pressable onPress={handleBackPress}>
              <Ionicons name="arrow-back" size={22} color="#D4AF37" />
            </Pressable>
          ),
          headerRight: () => (
            <Pressable
              onPress={handleRefresh}
              style={styles.headerButton}
            >
              <Ionicons name="refresh-outline" size={22} color="#D4AF37" />
            </Pressable>
          ),
        }}
      />

      <FlatList
        data={sellers}
        keyExtractor={(item) => item.sellerId}
        renderItem={({ item }: { item: any }) => (
          <SellerCard
            seller={item}
            onPress={() => handleSellerPress(item.sellerId, item.email)}
            onProductPress={handleProductPress}
            searchQuery={searchQuery}
          />
        )}
        ListHeaderComponent={renderHeader}
        ListEmptyComponent={renderEmptyState}
        contentContainerStyle={styles.listContent}
        showsVerticalScrollIndicator={false}
        keyboardDismissMode="on-drag"
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={handleRefresh}
            tintColor="#D4AF37"
            colors={['#D4AF37']}
          />
        }
        ItemSeparatorComponent={() => <View style={styles.separator} />}
        ListFooterComponent={<View style={styles.footer} />}
        initialNumToRender={5}
        maxToRenderPerBatch={10}
        windowSize={10}
      />
    </SafeAreaView>
  );
}

/* ---------------------------------- */
/* Styles */
/* ---------------------------------- */

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
    backgroundColor: '#F8F9FA',
  },
  header: {
    paddingTop: 16,
    paddingBottom: 8,
  },
  categoryHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingVertical: 16,
    backgroundColor: '#FFFFFF',
    marginHorizontal: 16,
    marginBottom: 16,
    borderRadius: 12,
    elevation: 2,
  },
  categoryIconContainer: {
    width: 56,
    height: 56,
    borderRadius: 28,
    backgroundColor: 'rgba(212,175,55,0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  categoryInfo: { flex: 1 },
  categoryTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#1A1A1A',
  },
  categoryDescription: {
    fontSize: 14,
    color: '#666',
    marginVertical: 6,
  },
  productCount: {
    fontSize: 13,
    color: '#D4AF37',
    fontWeight: '600',
  },
  searchBar: {
    marginHorizontal: 16,
    marginBottom: 16,
  },
  sortContainer: {
    paddingHorizontal: 16,
    marginBottom: 16,
  },
  sortLabel: {
    fontSize: 14,
    fontWeight: '600',
  },
  sortOptions: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
  },
  sortOption: {
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 16,
    backgroundColor: '#F0F0F0',
  },
  activeSortOption: {
    backgroundColor: '#D4AF37',
  },
  sortOptionText: {
    fontSize: 12,
    color: '#666',
  },
  activeSortOptionText: {
    color: '#FFF',
    fontWeight: '600',
  },
  resultsInfo: {
    paddingHorizontal: 24,
  },
  resultsText: {
    fontSize: 13,
    color: '#666',
    fontStyle: 'italic',
  },
  listContent: {
    flexGrow: 1,
    paddingBottom: Platform.OS === 'ios' ? 40 : 24,
  },
  separator: {
    height: 1,
    backgroundColor: 'rgba(0,0,0,0.05)',
    marginHorizontal: 24,
  },
  footer: { height: 60 },
  headerButton: {
    marginRight: 16,
  },
  clearSearchButton: {
    backgroundColor: '#F0F0F0',
    padding: 12,
    borderRadius: 8,
  },
  clearSearchText: {
    color: '#666',
  },
  becomeSellerButton: {
    backgroundColor: '#D4AF37',
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 8,
  },
  becomeSellerText: {
    color: '#FFF',
    fontWeight: '600',
  },
});
