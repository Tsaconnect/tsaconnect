// The "Promotions Arena" sub-heading on the Reward hub: a short live preview of
// the listings currently on promotion, with See all opening the full Arena.
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Card, PressableScale, Spinner, Text } from '@/components/ui';
import { Product } from '@/components/services/api';
import ProductCard from '@/components/marketplace/ProductCard';
import { usePromotions } from '@/hooks/usePromotions';

const PREVIEW_COUNT = 3;

export default function PromotionsArenaSection() {
  const { spacing } = useTheme();
  // ProductCard insets itself by spacing.lg; inside this padded card that doubles
  // up and pushes its action row off the edge, so the list cancels it out.
  const cancelCardInset = { marginHorizontal: -spacing.lg };
  const { items, loading, error } = usePromotions();

  const open = (p: Product) =>
    router.push(`/product/${p.id}?productData=${encodeURIComponent(JSON.stringify(p))}`);

  return (
    <Card padding="lg" style={{ marginBottom: spacing.lg }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: spacing.sm }}>
        <Text variant="title3" weight="700">
          Promotions Arena
        </Text>
        <PressableScale onPress={() => router.push('/promotions')} accessibilityRole="link" accessibilityLabel="See all promotions">
          <Text variant="bodySm" weight="600" color="accent">
            See all
          </Text>
        </PressableScale>
      </View>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
        Products on promotion. Bring verified referrals to unlock the discounted price.
      </Text>
      {loading ? (
        <Spinner />
      ) : items.length > 0 ? (
        <View style={cancelCardInset}>
          {items.slice(0, PREVIEW_COUNT).map((p) => (
            <ProductCard key={p.id} product={p} onPress={() => open(p)} />
          ))}
        </View>
      ) : (
        <Text variant="bodySm" color="textMuted">
          {error ? "Couldn't load promotions. Pull down to retry." : 'No promotions right now. Check back soon.'}
        </Text>
      )}
    </Card>
  );
}
