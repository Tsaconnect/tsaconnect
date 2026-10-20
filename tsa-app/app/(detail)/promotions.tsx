// app/(detail)/promotions.tsx
//
// Promotions Arena: every listing currently on promotion. Reachable from the
// drawer and the marketplace tab strip, so it lives in (detail) (tab ownership).
// Products only (the API refuses promos on services). Rows are the marketplace
// ProductCard and open the product detail.
import React, { useCallback } from 'react';
import { ActivityIndicator, FlatList, RefreshControl } from 'react-native';
import { useRouter } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Product } from '@/components/services/api';
import ProductCard from '@/components/marketplace/ProductCard';
import { usePromotions } from '@/hooks/usePromotions';
import { useTheme } from '@/theme';
import { EmptyState, ErrorState, Spinner } from '@/components/ui';

export default function PromotionsArenaScreen() {
  const { colors, spacing } = useTheme();
  const router = useRouter();
  const { items, loading, refreshing, loadingMore, error, refresh, loadMore } = usePromotions();

  const open = useCallback(
    (p: Product) => router.push(`/product/${p.id}?productData=${encodeURIComponent(JSON.stringify(p))}`),
    [router],
  );

  let body: React.ReactNode;
  if (loading) {
    body = <Spinner />;
  } else if (error && items.length === 0) {
    body = <ErrorState title="Couldn't load promotions" subtitle={error} onRetry={refresh} />;
  } else {
    body = (
      <FlatList
        data={items}
        keyExtractor={(p) => p.id}
        renderItem={({ item }) => <ProductCard product={item} onPress={() => open(item)} />}
        onEndReached={loadMore}
        onEndReachedThreshold={0.5}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={colors.accent} />}
        contentContainerStyle={{ paddingTop: spacing.sm, paddingBottom: spacing.xl, flexGrow: 1 }}
        ListEmptyComponent={
          <EmptyState icon="pricetag" title="No promotions right now" subtitle="Check back soon." />
        }
        ListFooterComponent={
          loadingMore ? <ActivityIndicator color={colors.accent} style={{ padding: spacing.lg }} /> : null
        }
      />
    );
  }

  return (
    <SafeAreaView edges={[]} style={{ flex: 1, backgroundColor: colors.background }}>
      {body}
    </SafeAreaView>
  );
}
