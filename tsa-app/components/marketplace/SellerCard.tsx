// components/marketplace/SellerCard.tsx
//
// Seller card — a product-grid hero with seller attribution footer, used in the
// subcategory/search seller list. Migrated onto the TSA Signature design system
// (theme tokens via useTheme + @/components/ui primitives). All logic is
// unchanged: the props interface + default export, the search-highlight helper,
// price formatting, product-image resolution, the derived seller/product values,
// role/status gating (active vs inactive), and every navigation callback
// (onPress for the seller, onProductPress for products + the cart overlay with
// its stopPropagation) are preserved verbatim. This is a visual / structural
// restyle only.
import React from 'react';
import { View, Image, Dimensions } from 'react-native';
import { useTheme } from '@/theme';
import {
  Avatar,
  Badge,
  Divider,
  Icon,
  PressableScale,
  Surface,
  Text,
} from '@/components/ui';

const { width: SCREEN_WIDTH } = Dimensions.get('window');

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
  const { colors, spacing, radius } = useTheme();

  // Two-up grid sized off the screen width minus the card margin + padding + gap.
  const tileWidth =
    (SCREEN_WIDTH - spacing.lg * 2 - spacing.lg * 2 - spacing.sm) / 2;

  const highlightText = (text: string) => {
    if (!searchQuery || !text) return text;
    const parts = text.split(new RegExp(`(${searchQuery})`, 'gi'));
    return (
      <Text>
        {parts.map((part, index) =>
          part.toLowerCase() === searchQuery.toLowerCase() ? (
            <Text key={index} weight="700" style={{ backgroundColor: colors.warningBg, color: colors.textPrimary }}>
              {part}
            </Text>
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
  const visibleProducts = (seller.products || []).slice(0, 4);
  const totalCount = seller.productCount || seller.products?.length || 0;
  const hasMoreProducts = totalCount > 4;
  const hasRating = seller.reviewCount != null && seller.reviewCount > 0;
  const isActive = (seller.products || []).some((p: any) => p.status === 'active');

  return (
    <Surface
      elevation="sm"
      style={{ marginHorizontal: spacing.lg, marginVertical: spacing.sm, overflow: 'hidden' }}
    >
      {/* ===== Product Grid (Hero) ===== */}
      <View
        style={{
          flexDirection: 'row',
          flexWrap: 'wrap',
          padding: spacing.lg,
          gap: spacing.sm,
        }}
      >
        {visibleProducts.map((product: any, index: number) => {
          const imageUrl = getProductImage(product);
          const isFull = visibleProducts.length === 1;
          return (
            <PressableScale
              key={product._id || product.id || `p-${index}`}
              style={{ width: isFull ? '100%' : tileWidth }}
              onPress={() => onProductPress(product._id || product.id, product)}
            >
              {/* Product Image */}
              <View
                style={{
                  width: '100%',
                  aspectRatio: 1,
                  maxHeight: 200,
                  borderRadius: radius.md,
                  backgroundColor: colors.surfaceAlt,
                  overflow: 'hidden',
                }}
              >
                {imageUrl ? (
                  <Image source={{ uri: imageUrl }} style={{ width: '100%', height: '100%' }} resizeMode="cover" />
                ) : (
                  <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
                    <Icon name="image" size={36} color={colors.textMuted} />
                  </View>
                )}
                {/* Cart overlay */}
                <PressableScale
                  style={{
                    position: 'absolute',
                    bottom: spacing.sm,
                    right: spacing.sm,
                    width: 30,
                    height: 30,
                    borderRadius: radius.pill,
                    backgroundColor: colors.surface,
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                  onPress={(e) => {
                    e.stopPropagation();
                    onProductPress(product._id || product.id, product);
                  }}
                  hitSlop={{ top: 6, bottom: 6, left: 6, right: 6 }}
                >
                  <Icon name="cart" size={14} color={colors.accent} />
                </PressableScale>
              </View>

              {/* Product Info */}
              <View style={{ paddingTop: spacing.sm, paddingBottom: spacing.xs }}>
                <Text variant="bodySm" weight="600" numberOfLines={2} style={{ marginBottom: spacing.xs }}>
                  {searchQuery ? highlightText(product.name) : product.name}
                </Text>
                {product.condition && (
                  <Badge label={product.condition} size="sm" style={{ marginBottom: spacing.xs }} />
                )}
                <View style={{ flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: spacing.xs }}>
                  <Text variant="bodyMed" weight="700" color="accent" numeric>
                    {formatPrice(product.price)}
                  </Text>
                  {product.type === 'Service' && (
                    <Text variant="caption" weight="500" color="danger">
                      0.1$ contact fee
                    </Text>
                  )}
                </View>
              </View>
            </PressableScale>
          );
        })}
      </View>

      {/* View All */}
      {hasMoreProducts && (
        <>
          <Divider />
          <PressableScale
            onPress={onPress}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              paddingVertical: spacing.md,
              gap: spacing.xs,
            }}
          >
            <Text variant="bodySm" weight="600" color="accent">
              View all {totalCount} products
            </Text>
            <Icon name="arrowRight" size={14} color={colors.accent} />
          </PressableScale>
        </>
      )}

      {/* ===== Seller Attribution (Footer) ===== */}
      <Divider />
      <PressableScale
        onPress={onPress}
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          paddingHorizontal: spacing.lg,
          paddingVertical: spacing.md,
          backgroundColor: colors.backgroundAlt,
        }}
      >
        <Avatar name={sellerDisplayName} size={26} />
        <View style={{ flex: 1, marginLeft: spacing.sm }}>
          <Text variant="bodySm" weight="500" color="textSecondary" numberOfLines={1}>
            <Text variant="bodySm" weight="600" color="success">Offers by </Text>
            {sellerDisplayName}
          </Text>
          {hasRating && (
            <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: 2, gap: 3 }}>
              <Icon name="star" size={11} color={colors.warning} />
              <Text variant="caption" weight="600" color="warning">{seller.rating?.toFixed(1)}</Text>
              <Text variant="caption" color="textMuted">({seller.reviewCount} {seller.reviewCount === 1 ? 'review' : 'reviews'})</Text>
            </View>
          )}
        </View>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Badge
            label={isActive ? 'Active' : 'Inactive'}
            size="sm"
            color={isActive ? colors.escrow : colors.textSecondary}
            background={isActive ? colors.escrowBg : colors.surfaceAlt}
          />
          <Text variant="caption" weight="500" color="accent">
            {totalCount} {totalCount === 1 ? 'Product' : 'Products'}
          </Text>
          <Icon name="chevronRight" size={14} color={colors.accent} />
        </View>
      </PressableScale>
    </Surface>
  );
};

export default SellerCard;
