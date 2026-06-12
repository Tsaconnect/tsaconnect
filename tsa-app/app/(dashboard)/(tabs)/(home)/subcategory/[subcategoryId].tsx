// app/(dashboard)/(tabs)/(home)/subcategory/[subcategoryId].tsx
//
// Subcategory products — sellers (with their products) within a subcategory or
// category tree. Restyled onto the TSA Signature design system (@/theme tokens
// + @/components/ui primitives). Data fetching (useSubcategoryProducts hook),
// the debounced SearchBar-driven search/filtering, sort state, role/service
// gating, the SellerCard rendering, and every navigation route + param are
// preserved verbatim — this is a visual / structural migration only.
import React, { useState, useCallback, useMemo } from 'react';
import { View, FlatList, RefreshControl, Image } from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { SearchBar } from '@/components/common/SearchBar';
import { useSubcategoryProducts } from '@/hooks/useSubcategoryProducts';
import { SellerCard } from '@/components/marketplace/SellerCard';
import { useTheme } from '@/theme';
import {
  Card,
  Chip,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  Spinner,
  Text,
} from '@/components/ui';

const SORT_OPTIONS = [
  'recent',
  'popular',
  'price_low',
  'price_high',
  'rating',
] as const;

type SortOption = typeof SORT_OPTIONS[number];

const SORT_LABELS: Record<SortOption, string> = {
  recent: 'Recent',
  popular: 'Popular',
  price_low: 'Price: Low to High',
  price_high: 'Price: High to Low',
  rating: 'Rating',
};

/* ---------------------------------- */
/* Component */
/* ---------------------------------- */

export default function SubcategorySellersScreen() {
  const { colors, spacing, radius } = useTheme();

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
    // Filter to the section being browsed so a "Both" category doesn't leak
    // services into the product list (or vice-versa).
    type: isService ? 'Service' : 'Product',
  });

  const baseHeaderOptions = useMemo(
    () => ({
      headerShown: true,
      headerBackTitle: 'Back',
      headerTintColor: colors.accent,
      headerStyle: { backgroundColor: colors.surface },
      headerTitleStyle: {
        fontWeight: '600' as const,
        color: colors.textPrimary,
        fontSize: 16,
      },
    }),
    [colors.accent, colors.surface, colors.textPrimary]
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
      // Services need their own detail screen (contact-fee unlock, no cart);
      // route by item type when available, falling back to the screen flag.
      const treatAsService =
        productData?.type === 'Service' || (!productData?.type && isService);
      if (treatAsService) {
        const heroImage =
          productData?.images?.find?.((img: any) => img?.url)?.url ||
          (typeof productData?.image === 'string' ? productData.image : '') ||
          '';
        router.push({
          pathname: '/servicedetail',
          params: {
            id: productId,
            title: productData?.name ?? '',
            description: productData?.description ?? '',
            image: heroImage,
          },
        });
        return;
      }
      if (productData) {
        router.push(`/product/${productId}?productData=${encodeURIComponent(JSON.stringify(productData))}`);
      } else {
        router.push(`/product/${productId}`);
      }
    },
    [router, isService]
  );

  const handleBackPress = useCallback(() => {
    if (router.canGoBack()) {
      router.back();
    } else {
      router.push('/digital');
    }
  }, [router]);

  const renderHeader = useCallback(
    () => (
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.lg, gap: spacing.lg }}>
        {/* Category Info */}
        <Card padding="lg">
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.lg }}>
            {categoryInfo?.image ? (
              <Image
                source={{ uri: categoryInfo.image }}
                style={{
                  width: 56,
                  height: 56,
                  borderRadius: radius.pill,
                  backgroundColor: colors.surfaceAlt,
                }}
                resizeMode="cover"
              />
            ) : (
              <View
                style={{
                  width: 56,
                  height: 56,
                  borderRadius: radius.pill,
                  backgroundColor: colors.accentMuted,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
              >
                <Icon name="storefront" size={28} color={colors.accent} />
              </View>
            )}

            <View style={{ flex: 1 }}>
              <Text variant="title3" weight="700" numberOfLines={1}>
                {subcategoryName || categoryInfo?.title || 'Products'}
              </Text>

              <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                {categoryInfo?.description ||
                  (isService ? 'Browse services in this category' : 'Browse products in this category')}
              </Text>

              {totalProducts > 0 && (
                <Text variant="caption" weight="600" color="accent" style={{ marginTop: spacing.xs }}>
                  {totalProducts} {isService ? 'services' : 'products'} available
                </Text>
              )}
            </View>
          </View>
        </Card>

        {/* Search */}
        <SearchBar
          placeholder="Search sellers, products, or emails..."
          onSearch={handleSearch}
          initialValue={searchQuery}
          debounceDelay={400}
        />

        {/* Sort */}
        <View style={{ gap: spacing.sm }}>
          <Text variant="bodySm" weight="600">
            Sort by:
          </Text>
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
            {SORT_OPTIONS.map((option) => (
              <Chip
                key={option}
                label={SORT_LABELS[option]}
                selected={sortOption === option}
                onPress={() => setSortOption(option)}
              />
            ))}
          </View>
        </View>

        {/* Results */}
        {!loading && (
          <Text variant="caption" color="textSecondary">
            Showing {showingProducts} of {totalProducts} {isService ? 'services' : 'products'}
            {searchQuery ? ` for "${searchQuery}"` : ''}
          </Text>
        )}
      </View>
    ),
    [
      categoryInfo,
      subcategoryName,
      searchQuery,
      sortOption,
      totalProducts,
      showingProducts,
      loading,
      isService,
      colors,
      spacing,
      radius,
      handleSearch,
    ]
  );

  /* ---------------------------------- */
  /* Empty State */
  /* ---------------------------------- */

  const renderEmptyState = () => {
    if (searchQuery) {
      return (
        <EmptyState
          icon="search"
          title="No Results Found"
          subtitle={`No sellers or products match "${searchQuery}"`}
          actionLabel="Clear search"
          onAction={() => setSearchQuery('')}
        />
      );
    }

    return (
      <EmptyState
        icon="storefront"
        title={isService ? 'No Services Available' : 'No Products Available'}
        subtitle={
          isService
            ? 'There are no services in this category yet.'
            : 'There are no products in this category yet.'
        }
        actionLabel={isMerchant ? (isService ? 'List a Service' : 'List a Product') : 'Become a Merchant'}
        onAction={() =>
          isMerchant
            ? router.push(isService ? '/serviceaction?index=1' : '/merchants/inventory/add')
            : router.push('/merchants/merchant-request')
        }
      />
    );
  };

  /* ---------------------------------- */
  /* Loading */
  /* ---------------------------------- */

  if (loading && !refreshing && sellers.length === 0) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
        <Stack.Screen
          options={{
            ...baseHeaderOptions,
            title: subcategoryName || categoryTitle || 'Loading...',
          }}
        />
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  /* ---------------------------------- */
  /* Error */
  /* ---------------------------------- */

  if (error) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
        <Stack.Screen
          options={{
            ...baseHeaderOptions,
            title: subcategoryName || categoryTitle || 'Error',
          }}
        />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <ErrorState
            subtitle={error || 'Failed to load sellers. Please try again.'}
            onRetry={handleRefresh}
          />
        </View>
      </SafeAreaView>
    );
  }

  /* ---------------------------------- */
  /* Main Render */
  /* ---------------------------------- */

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
      <Stack.Screen
        options={{
          ...baseHeaderOptions,
          title: subcategoryName || categoryTitle || (isService ? 'Services' : 'Products'),
          headerLeft: () => (
            <IconButton
              icon="chevronLeft"
              accessibilityLabel="Go back"
              color={colors.accent}
              size={24}
              onPress={handleBackPress}
            />
          ),
          headerRight: () => (
            <IconButton
              icon="refresh"
              accessibilityLabel="Refresh"
              color={colors.accent}
              size={20}
              onPress={handleRefresh}
            />
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
        contentContainerStyle={{ flexGrow: 1, paddingBottom: spacing['4xl'] }}
        showsVerticalScrollIndicator={false}
        keyboardDismissMode="on-drag"
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={handleRefresh}
            tintColor={colors.accent}
            colors={[colors.accent]}
          />
        }
        ItemSeparatorComponent={() => <Divider spacing="xs" />}
        ListFooterComponent={<View style={{ height: spacing['4xl'] }} />}
        initialNumToRender={5}
        maxToRenderPerBatch={10}
        windowSize={10}
      />
    </SafeAreaView>
  );
}
