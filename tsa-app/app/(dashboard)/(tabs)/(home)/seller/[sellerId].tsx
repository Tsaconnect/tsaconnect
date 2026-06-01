// app/(dashboard)/(tabs)/(home)/seller/[sellerId].tsx
//
// Seller public profile + their product/service catalogue. Restyled onto the TSA
// Signature design system (@/theme tokens + @/components/ui primitives). All data
// fetching (useSellerData), currency formatting (useCurrency + formatPrice), the
// product-vs-service navigation split, contact/call/share/back handlers, and the
// FlatList config are preserved verbatim — this is a visual / structural migration
// only. The keyExtractor now coerces the (possibly-undefined) item key to a string,
// keeping the prior behavior while clearing the pre-existing TS error.
import React, { useCallback } from 'react';
import {
  View,
  FlatList,
  Image,
  RefreshControl,
} from 'react-native';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useSellerData } from '@/hooks/useSellerData';
import { useCurrency } from '@/contexts/CurrencyContext';
import { formatPrice as formatPriceUtil } from '@/utils/formatPrice';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Avatar,
  Badge,
  Button,
  Card,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Surface,
  Text,
} from '@/components/ui';

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
  const { colors, spacing } = useTheme();
  const { currency, rate } = useCurrency();

  const fmtPrice = (usd: number) => formatPriceUtil(usd, { currency, rate });

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

  const handleProductPress = useCallback((productId: string, productData?: any) => {
    // A merchant's catalogue mixes products and services; services have their
    // own detail screen and shouldn't render with the cart/stock UI.
    if (productData?.type === 'Service') {
      const heroImage =
        productData.images?.find?.((img: any) => img?.url)?.url ||
        (typeof productData.image === 'string' ? productData.image : '') ||
        '';
      router.push({
        pathname: '/servicedetail',
        params: {
          id: productId,
          title: productData.name ?? '',
          description: productData.description ?? '',
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

  const showCall =
    !!sellerData?.phoneNumber &&
    sellerData.phoneNumber !== 'N/A' &&
    sellerData.phoneNumber !== 'Contact for details';

  const renderProduct = useCallback(({ item }: { item: any }) => (
    <PressableScale
      onPress={() => handleProductPress(item._id || item.id, item)}
      style={{ flex: 1, marginHorizontal: spacing.xs }}
    >
      <Surface elevation="sm" style={{ overflow: 'hidden' }}>
        <View style={{ position: 'relative' }}>
          {item.images?.[0]?.url ? (
            <Image
              source={{ uri: item.images[0].url }}
              style={{ width: '100%', aspectRatio: 1, backgroundColor: colors.surfaceAlt }}
              resizeMode="cover"
            />
          ) : (
            <View
              style={{
                width: '100%',
                aspectRatio: 1,
                backgroundColor: colors.surfaceAlt,
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <Icon name="image" size={32} color={colors.textMuted} />
            </View>
          )}
          {item.isFeatured && (
            <Badge
              label="FEATURED"
              size="sm"
              color={colors.onAccent}
              background={colors.accent}
              style={{ position: 'absolute', top: spacing.sm, right: spacing.sm }}
            />
          )}
        </View>
        <View style={{ padding: spacing.md }}>
          <Text variant="bodySm" weight="700" numberOfLines={2} style={{ minHeight: 40 }}>
            {item.name}
          </Text>
          <Text variant="caption" color="textSecondary" numberOfLines={2} style={{ marginTop: spacing.xs, minHeight: 32 }}>
            {item.description}
          </Text>
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginTop: spacing.sm,
            }}
          >
            <Text variant="bodyMed" weight="700" color="accent" numeric>
              {fmtPrice(item.price || 0)}
            </Text>
            {item.stock !== undefined && (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}>
                <Icon name="pool" size={12} color={item.stock > 0 ? colors.success : colors.danger} />
                <Text variant="overline" color={item.stock > 0 ? 'success' : 'danger'}>
                  {item.stock > 0 ? `${item.stock} in stock` : 'Out of stock'}
                </Text>
              </View>
            )}
          </View>
          {item.rating?.average > 0 && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: 4, marginTop: spacing.xs }}>
              <Icon name="star" size={12} color={colors.accent} />
              <Text variant="overline" color="textSecondary">
                {item.rating.average.toFixed(1)} ({item.rating.count || 0})
              </Text>
            </View>
          )}
        </View>
      </Surface>
    </PressableScale>
  ), [handleProductPress, fmtPrice, colors, spacing]);

  const renderHeader = useCallback(() => {
    if (!sellerData) return null;

    const detailRows: { icon: Parameters<typeof Icon>[0]['name']; text: string }[] = [
      { icon: 'nearMe', text: sellerData.location },
      { icon: 'send', text: sellerData.email },
      ...(sellerData.phoneNumber ? [{ icon: 'mobile' as const, text: sellerData.phoneNumber }] : []),
      { icon: 'clock', text: `Joined ${new Date(sellerData.joinedDate).toLocaleDateString()}` },
    ];

    const stats: { value: string; label: string }[] = [
      { value: String(sellerData.productCount), label: 'Products' },
      { value: sellerData.totalSales?.toLocaleString() || '0', label: 'Sales' },
      { value: sellerData.rating.average.toFixed(1), label: 'Rating' },
      { value: String(Math.floor(sellerData.rating.count)), label: 'Reviews' },
    ];

    return (
      <View style={{ padding: spacing.lg, gap: spacing.lg }}>
        {/* Profile */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.lg }}>
            <Avatar name={sellerData.name} uri={sellerData.profilePhoto} size={72} />
            <View style={{ flex: 1 }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Text variant="title2" weight="700" numberOfLines={1} style={{ flexShrink: 1 }}>
                  {sellerData.name}
                </Text>
                {sellerData.isVerified && (
                  <Icon name="verified" size={20} color={colors.success} />
                )}
              </View>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.xs }}>
                <Icon name="star" size={16} color={colors.accent} />
                <Text variant="bodySm" weight="700" color="accent">
                  {sellerData.rating.average.toFixed(1)}
                </Text>
                <Text variant="caption" color="textMuted">
                  ({sellerData.rating.count} reviews)
                </Text>
              </View>
            </View>
          </View>

          <Divider spacing="lg" />

          {/* Contact actions */}
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button
                title="Message"
                leftIcon="send"
                onPress={handleContactSeller}
              />
            </View>
            {showCall && (
              <View style={{ flex: 1 }}>
                <Button
                  title="Call"
                  leftIcon="mobile"
                  variant="success"
                  onPress={handleCallSeller}
                />
              </View>
            )}
          </View>
        </Card>

        {/* Seller details */}
        <Card background="surfaceAlt" elevation="none">
          <View style={{ gap: spacing.md }}>
            {detailRows.map((row, i) => (
              <View key={i} style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name={row.icon} size={18} color={colors.textMuted} />
                <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                  {row.text}
                </Text>
              </View>
            ))}
          </View>
        </Card>

        {/* About */}
        {sellerData.description && (
          <View>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
              About This Seller
            </Text>
            <Text variant="bodySm" color="textSecondary">
              {sellerData.description}
            </Text>
          </View>
        )}

        {/* Stats */}
        <View style={{ flexDirection: 'row', gap: spacing.sm }}>
          {stats.map((stat) => (
            <AccentCard key={stat.label} padding="md" style={{ flex: 1 }}>
              <Text variant="title3" weight="700" color="accent" align="center">
                {stat.value}
              </Text>
              <Text variant="overline" color="textSecondary" align="center" style={{ marginTop: spacing.xs }}>
                {stat.label}
              </Text>
            </AccentCard>
          ))}
        </View>

        {/* Catalogue header */}
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="title2" weight="700">
            Product Catalogue
          </Text>
          <Badge
            label={`${sellerData.products.length} ${sellerData.products.length === 1 ? 'product' : 'products'}`}
            color={colors.accent}
            background={colors.accentMuted}
          />
        </View>
      </View>
    );
  }, [sellerData, handleContactSeller, handleCallSeller, showCall, colors, spacing]);

  const renderEmptyState = useCallback(() => (
    <View style={{ flex: 1, justifyContent: 'center' }}>
      <EmptyState
        icon="cart"
        title="No Products Yet"
        subtitle={`${sellerData?.name ?? 'This seller'} hasn't listed any products yet.`}
        actionLabel="Browse Other Sellers"
        onAction={() => router.push('/marketplace')}
      />
    </View>
  ), [sellerData, router]);

  if (loading && !sellerData) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
        <Stack.Screen
          options={{
            title: 'Loading...',
            headerShown: true,
            headerBackTitle: 'Back',
            headerTintColor: colors.accent,
            headerStyle: { backgroundColor: colors.surface },
          }}
        />
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', padding: spacing.xl }}>
          <Spinner size="large" label="Loading seller information..." />
        </View>
      </SafeAreaView>
    );
  }

  if (error && !sellerData) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
        <Stack.Screen
          options={{
            title: 'Error',
            headerShown: true,
          }}
        />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <ErrorState subtitle={error} onRetry={handleRefresh} />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'left', 'right']}>
      <Stack.Screen
        options={{
          title: sellerData?.name || 'Seller',
          headerShown: true,
          headerBackTitle: 'Back',
          headerTintColor: colors.accent,
          headerStyle: { backgroundColor: colors.surface },
          headerTitleStyle: {
            fontWeight: '600',
            color: colors.textPrimary,
            fontSize: 16,
          },
          headerRight: () => (
            <IconButton
              icon="send"
              accessibilityLabel="Share seller"
              color={colors.accent}
              size={20}
              onPress={handleShareSeller}
            />
          ),
        }}
      />

      <FlatList
        data={sellerData?.products || []}
        numColumns={2}
        keyExtractor={(item) => String(item._id ?? item.id ?? item.name)}
        renderItem={renderProduct}
        ListHeaderComponent={renderHeader}
        ListEmptyComponent={renderEmptyState}
        columnWrapperStyle={{ paddingHorizontal: spacing.lg - spacing.xs }}
        contentContainerStyle={[
          { paddingBottom: spacing['4xl'], gap: spacing.lg },
          (!sellerData?.products || sellerData.products.length === 0) && { flexGrow: 1 },
        ]}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={handleRefresh}
            tintColor={colors.accent}
            colors={[colors.accent]}
          />
        }
        initialNumToRender={8}
        maxToRenderPerBatch={10}
        windowSize={10}
      />
    </SafeAreaView>
  );
}
