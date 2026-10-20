// app/components/marketplace/ProductCard.tsx
//
// Product card (list + grid variants) used across marketplace / home / category
// lists. Migrated onto the TSA Signature design system (theme tokens +
// @/components/ui primitives). The props interface, exports, onPress/navigation
// wiring, image rendering (primaryImage + onError fallback), and all price /
// rating / stock computations are preserved verbatim — this is a visual /
// structural restyle only.
import React, { useState } from 'react';
import { View, Image } from 'react-native';
import { Product } from '@/components/services/api';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice, formatPriceDual } from '@/utils/formatPrice';
import { useTheme } from '@/theme';
import { PromoPrice } from '@/components/promotion/PromoPrice';
import {
    Badge,
    Card,
    Icon,
    PressableScale,
    Text,
} from '@/components/ui';

interface ProductCardProps {
    product: Product;
    onPress: () => void;
    variant?: 'list' | 'grid';
}

const ProductCard: React.FC<ProductCardProps> = ({ product, onPress, variant = 'list' }) => {
    const [imageError, setImageError] = useState(false);
    const { currency, rate } = useCurrency();
    const { colors, spacing, radius } = useTheme();

    const primaryImage = product.images?.[0]?.url;
    const formatLocal = (usd: number) => formatPrice(usd, { currency, rate });
    const dualPrice = formatPriceDual(product.price, rate, currency);
    const rating = product.rating?.average || 0;
    const ratingCount = product.rating?.count || 0;
    const isInStock = product.stock > 0;

    if (variant === 'grid') {
        return (
            <PressableScale onPress={onPress} style={{ width: '100%' }}>
                <Card style={{ overflow: 'hidden', padding: 0 }}>
                    {/* Image */}
                    <View style={{ position: 'relative', width: '100%', height: 160 }}>
                        {primaryImage && !imageError ? (
                            <Image
                                source={{ uri: primaryImage }}
                                style={{ width: '100%', height: '100%' }}
                                resizeMode="cover"
                                onError={() => setImageError(true)}
                            />
                        ) : (
                            <View
                                style={{
                                    width: '100%',
                                    height: '100%',
                                    backgroundColor: colors.surfaceAlt,
                                    justifyContent: 'center',
                                    alignItems: 'center',
                                }}
                            >
                                <Icon name="image" size={32} color={colors.textMuted} />
                            </View>
                        )}

                        {/* Stock Status */}
                        {!isInStock && (
                            <View style={{ position: 'absolute', top: spacing.sm, left: spacing.sm }}>
                                <Badge label="Out of Stock" color={colors.onAccent} background={colors.danger} size="sm" />
                            </View>
                        )}

                        {/* Featured Badge */}
                        {product.isFeatured && (
                            <View
                                style={{
                                    position: 'absolute',
                                    top: spacing.sm,
                                    right: spacing.sm,
                                    width: 24,
                                    height: 24,
                                    borderRadius: radius.pill,
                                    backgroundColor: colors.accent,
                                    justifyContent: 'center',
                                    alignItems: 'center',
                                }}
                            >
                                <Icon name="star" size={12} color={colors.onAccent} />
                            </View>
                        )}
                    </View>

                    {/* Product Info */}
                    <View style={{ padding: spacing.md }}>
                        <Text variant="bodySm" weight="600" numberOfLines={2} style={{ marginBottom: spacing.xs }}>
                            {product.name}
                        </Text>

                        <View style={{ marginBottom: spacing.xs }}>
                            <PromoPrice product={product} format={formatLocal} />
                        </View>

                        {rating > 0 && (
                            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                                <Icon name="star" size={12} color={colors.warning} />
                                <Text variant="caption" weight="600" style={{ marginLeft: spacing.xs, marginRight: 2 }}>
                                    {rating.toFixed(1)}
                                </Text>
                                <Text variant="caption" color="textMuted">
                                    ({ratingCount})
                                </Text>
                            </View>
                        )}

                        {/* Quick Actions */}
                        <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.sm }}>
                            <PressableScale
                                style={{
                                    width: 32,
                                    height: 32,
                                    borderRadius: radius.pill,
                                    backgroundColor: colors.surfaceAlt,
                                    justifyContent: 'center',
                                    alignItems: 'center',
                                }}
                            >
                                <Icon name="heartOutline" size={16} color={colors.textSecondary} />
                            </PressableScale>
                            <PressableScale
                                style={{
                                    width: 32,
                                    height: 32,
                                    borderRadius: radius.pill,
                                    backgroundColor: colors.surfaceAlt,
                                    justifyContent: 'center',
                                    alignItems: 'center',
                                }}
                            >
                                <Icon name="cart" size={16} color={colors.textSecondary} />
                            </PressableScale>
                        </View>
                    </View>
                </Card>
            </PressableScale>
        );
    }

    // List View
    return (
        <PressableScale onPress={onPress} style={{ marginHorizontal: spacing.lg, marginBottom: spacing.md }}>
            <Card padding="md" style={{ flexDirection: 'row' }}>
                {/* Image */}
                <View style={{ position: 'relative', width: 100, height: 100, marginRight: spacing.md }}>
                    {primaryImage && !imageError ? (
                        <Image
                            source={{ uri: primaryImage }}
                            style={{ width: '100%', height: '100%', borderRadius: radius.sm }}
                            resizeMode="cover"
                            onError={() => setImageError(true)}
                        />
                    ) : (
                        <View
                            style={{
                                width: '100%',
                                height: '100%',
                                borderRadius: radius.sm,
                                backgroundColor: colors.surfaceAlt,
                                justifyContent: 'center',
                                alignItems: 'center',
                            }}
                        >
                            <Icon name="image" size={40} color={colors.textMuted} />
                        </View>
                    )}

                    {/* Stock Status */}
                    {!isInStock && (
                        <View style={{ position: 'absolute', top: spacing.xs, left: spacing.xs }}>
                            <Badge label="Out of Stock" color={colors.onAccent} background={colors.danger} size="sm" />
                        </View>
                    )}
                </View>

                {/* Product Info */}
                <View style={{ flex: 1 }}>
                    <View
                        style={{
                            flexDirection: 'row',
                            justifyContent: 'space-between',
                            alignItems: 'flex-start',
                            marginBottom: spacing.xs,
                        }}
                    >
                        <View style={{ flex: 1, marginRight: spacing.sm }}>
                            <Text variant="bodyMed" weight="600" numberOfLines={2} style={{ marginBottom: spacing.xs }}>
                                {product.name}
                            </Text>
                            {product.isFeatured && (
                                <Badge label="Featured" icon="star" color={colors.onAccent} background={colors.accent} size="sm" />
                            )}
                        </View>

                        <PromoPrice product={product} format={formatLocal} />
                    </View>

                    <Text variant="bodySm" color="textSecondary" numberOfLines={2} style={{ marginBottom: spacing.sm }}>
                        {product.description}
                    </Text>

                    {/* Rating and Location */}
                    <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.sm }}>
                        {rating > 0 && (
                            <View style={{ flexDirection: 'row', alignItems: 'center', marginRight: spacing.md }}>
                                <Icon name="star" size={14} color={colors.warning} />
                                <Text variant="caption" weight="600" style={{ marginLeft: spacing.xs, marginRight: 2 }}>
                                    {rating.toFixed(1)}
                                </Text>
                                <Text variant="caption" color="textMuted">
                                    ({ratingCount})
                                </Text>
                            </View>
                        )}

                        <View style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
                            <Icon name="nearMe" size={12} color={colors.textSecondary} />
                            <Text variant="caption" color="textSecondary" numberOfLines={1} style={{ marginLeft: spacing.xs, flex: 1 }}>
                                {product.location}
                            </Text>
                        </View>
                    </View>

                    {/* Seller Info */}
                    {product.companyName && (
                        <View style={{ marginBottom: spacing.sm }}>
                            <Text variant="caption" color="textSecondary" style={{ fontStyle: 'italic' }}>
                                Sold by: {product.companyName}
                            </Text>
                        </View>
                    )}

                    {/* Quick Actions */}
                    <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.sm }}>
                        <PressableScale
                            style={{
                                flexDirection: 'row',
                                alignItems: 'center',
                                paddingHorizontal: spacing.md,
                                paddingVertical: spacing.sm,
                                borderRadius: radius.sm,
                                backgroundColor: colors.surfaceAlt,
                            }}
                        >
                            <Icon name="heartOutline" size={18} color={colors.textSecondary} />
                            <Text variant="bodySm" weight="500" color="textSecondary" style={{ marginLeft: spacing.xs }}>
                                Wishlist
                            </Text>
                        </PressableScale>

                        <PressableScale
                            style={{
                                flexDirection: 'row',
                                alignItems: 'center',
                                paddingHorizontal: spacing.lg,
                                paddingVertical: spacing.sm,
                                borderRadius: radius.sm,
                                backgroundColor: colors.accentMuted,
                                borderWidth: 1,
                                borderColor: colors.accent,
                            }}
                        >
                            <Icon name="cart" size={18} color={colors.accent} />
                            <Text variant="bodySm" weight="500" color="accent" style={{ marginLeft: spacing.xs }}>
                                Add to Cart
                            </Text>
                        </PressableScale>
                    </View>
                </View>
            </Card>
        </PressableScale>
    );
};

export default ProductCard;
