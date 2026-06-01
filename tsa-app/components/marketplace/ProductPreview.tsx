// components/marketplace/ProductPreview.tsx
//
// Product preview card — a horizontal strip of product cards (with search-term
// highlighting, featured / low-stock badges, and a "view all" tile) shown on
// seller / search surfaces. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives). Props, exports, the search
// highlight logic, the 6-item slice, money formatting, and all navigation
// routes + params are unchanged — this is a visual / structural migration only.
import React, { memo } from 'react';
import { View, Image, ScrollView, Dimensions } from 'react-native';
import { Product } from '@/types/marketplace';
import { useRouter } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountText,
  Badge,
  Card,
  EmptyState,
  Icon,
  PressableScale,
  Surface,
  Text,
} from '@/components/ui';

interface ProductPreviewProps {
  products: Product[];
  searchQuery?: string;
}

const { width } = Dimensions.get('window');
const PRODUCT_PREVIEW_WIDTH = (width - 80) / 3;

export const ProductPreview = memo(function ProductPreview({ products, searchQuery }: ProductPreviewProps) {
  const router = useRouter();
  const { colors, spacing, radius } = useTheme();

  if (products.length === 0) {
    return <EmptyState icon="pricetag" title="No products available" />;
  }

  const handleProductPress = (productId: string) => {
    router.push(`/product/${productId}`);
  };

  const highlightText = (text: string, highlight?: string) => {
    if (!highlight || !text.toLowerCase().includes(highlight.toLowerCase())) {
      return (
        <Text variant="caption" weight="500" numberOfLines={1}>
          {text}
        </Text>
      );
    }

    const parts = text.split(new RegExp(`(${highlight})`, 'gi'));
    return (
      <Text variant="caption" weight="500" numberOfLines={1}>
        {parts.map((part, i) =>
          part.toLowerCase() === highlight.toLowerCase() ? (
            <Text
              key={i}
              variant="caption"
              weight="700"
              style={{ backgroundColor: colors.accentMuted, color: colors.accent }}
            >
              {part}
            </Text>
          ) : (
            <Text key={i} variant="caption" weight="500">
              {part}
            </Text>
          )
        )}
      </Text>
    );
  };

  return (
    <View style={{ marginTop: spacing.sm }}>
      <View
        style={{
          flexDirection: 'row',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: spacing.md,
          paddingHorizontal: spacing.xs,
        }}
      >
        <Text variant="title3" weight="700">
          Products
        </Text>
        <Badge label={`${products.length} items`} color={colors.accent} background={colors.accentMuted} size="sm" />
      </View>

      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={{ paddingRight: spacing.lg }}
      >
        {products.slice(0, 6).map((product) => (
          <PressableScale
            key={product.productId}
            onPress={() => handleProductPress(product.productId)}
            style={{ width: PRODUCT_PREVIEW_WIDTH, marginRight: spacing.md }}
          >
            <Surface elevation="sm" style={{ overflow: 'hidden' }}>
              <View style={{ position: 'relative' }}>
                <Image
                  source={{ uri: product.image }}
                  style={{
                    width: '100%',
                    height: PRODUCT_PREVIEW_WIDTH,
                    backgroundColor: colors.surfaceAlt,
                  }}
                  resizeMode="cover"
                />
                {product.isFeatured && (
                  <View style={{ position: 'absolute', top: spacing.sm, left: spacing.sm }}>
                    <Badge label="Featured" icon="bolt" color={colors.onAccent} background={colors.accent} size="sm" />
                  </View>
                )}
                {product.stock && product.stock < 10 && (
                  <View style={{ position: 'absolute', bottom: spacing.sm, right: spacing.sm }}>
                    <Badge label="Low Stock" color={colors.warning} background={colors.warningBg} size="sm" />
                  </View>
                )}
              </View>
              <View style={{ padding: spacing.sm }}>
                {highlightText(product.name, searchQuery)}
                <View
                  style={{
                    flexDirection: 'row',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    marginTop: spacing.xs,
                  }}
                >
                  <AmountText value={product.price} currency="USD" variant="bodySm" color="accent" minimumFractionDigits={2} maximumFractionDigits={2} />
                  {product.stock && (
                    <Text variant="overline" color="textMuted">
                      {product.stock} left
                    </Text>
                  )}
                </View>
              </View>
            </Surface>
          </PressableScale>
        ))}

        {products.length > 6 && (
          <PressableScale
            onPress={() => {
              // Navigate to full product list
              const sellerId = products[0]?.sellerId;
              if (sellerId) {
                router.push(`/seller/${sellerId}`);
              }
            }}
            style={{ width: PRODUCT_PREVIEW_WIDTH }}
          >
            <Card
              padding="md"
              style={{
                flex: 1,
                alignItems: 'center',
                justifyContent: 'center',
                borderWidth: 2,
                borderStyle: 'dashed',
                borderColor: colors.accentMuted,
                backgroundColor: colors.accentMuted,
                borderRadius: radius.lg,
              }}
            >
              <Icon name="list" size={24} color={colors.accent} />
              <Text variant="caption" color="textSecondary" align="center" style={{ marginTop: spacing.sm }}>
                View all
              </Text>
              <Text variant="overline" weight="700" color="accent" align="center">
                +{products.length - 6} more
              </Text>
            </Card>
          </PressableScale>
        )}
      </ScrollView>
    </View>
  );
});
