// app/(detail)/promotions.tsx
//
// Promotions Arena: every listing currently on promotion. Reachable from the
// drawer and the marketplace tab strip, so it lives in (detail) (tab ownership).
// Rows are the marketplace ProductCard and open the same detail screens the
// marketplace opens.
import React, { useCallback } from 'react';
import { ActivityIndicator, FlatList, RefreshControl, ScrollView, View } from 'react-native';
import { useRouter } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Product } from '@/components/services/api';
import ProductCard from '@/components/marketplace/ProductCard';
import { usePromotions, PromoFilter } from '@/hooks/usePromotions';
import { useTheme } from '@/theme';
import { Chip, EmptyState, ErrorState, Spinner } from '@/components/ui';

const FILTERS: { key: PromoFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'product', label: 'Products' },
  { key: 'order', label: 'Ordering' },
  { key: 'booking', label: 'Booking' },
  { key: 'contact', label: 'Contact' },
];

export default function PromotionsArenaScreen() {
  const { colors, spacing } = useTheme();
  const router = useRouter();
  const [filter, setFilter] = React.useState<PromoFilter>('all');
  const { items, loading, refreshing, loadingMore, error, refresh, loadMore } = usePromotions(filter);

  // Same destinations as the marketplace: services -> /servicedetail, else product.
  const open = useCallback(
    (p: Product) => {
      if (p.type === 'Service') {
        router.push({
          pathname: '/servicedetail',
          params: {
            id: p.id,
            title: p.name ?? '',
            description: p.description ?? '',
            image: p.images?.find((i) => i?.url)?.url ?? '',
          },
        });
        return;
      }
      router.push(`/product/${p.id}?productData=${encodeURIComponent(JSON.stringify(p))}`);
    },
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
      <View>
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingVertical: spacing.md, gap: spacing.sm }}
        >
          {FILTERS.map((f) => (
            <Chip key={f.key} label={f.label} selected={filter === f.key} onPress={() => setFilter(f.key)} />
          ))}
        </ScrollView>
      </View>
      {body}
    </SafeAreaView>
  );
}
