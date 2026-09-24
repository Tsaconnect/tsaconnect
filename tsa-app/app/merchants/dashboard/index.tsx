// app/merchants/dashboard/index.tsx
//
// Merchant dashboard — overview/stats/quick actions: a hero total-listings card,
// product & service KPI tiles, and recent product/service rows. Migrated onto the
// TSA Signature design system (@/theme tokens + @/components/ui primitives).
//
// Behavior is unchanged: the auth-token bootstrap, the getMerchantProducts fetch,
// the client-side product/service partition by `type`, all KPI computations, the
// flattened single-FlatList structure, navigation routes/params, and money
// formatting (formatPrice) are all preserved verbatim — visual/structural restyle only.
import React, { useState, useCallback } from 'react';
import { View, FlatList, RefreshControl, Image } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect } from 'expo-router';
import api, { Product } from '../../../components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useTheme } from '@/theme';
import { useIsPaymentMerchant } from '@/hooks/useIsPaymentMerchant';
import {
  AccentCard,
  Badge,
  Card,
  Icon,
  IconName,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

type ListItem =
  | { kind: 'header' }
  | { kind: 'product'; data: Product }
  | { kind: 'service'; data: Product }
  | { kind: 'sectionTitle'; title: string }
  | { kind: 'emptySection'; copy: string };

export default function MerchantDashboard() {
  const { colors, spacing, radius } = useTheme();
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [items, setItems] = useState<Product[]>([]);
  const [merchantName, setMerchantName] = useState('Merchant');
  const { formatPrice } = useCurrency();
  const { isPm, status: pmStatus, loading: pmLoading, reload: reloadPmStatus } = useIsPaymentMerchant();

  const loadDashboard = useCallback(async () => {
    try {
      const token = await AsyncStorage.getItem('token');
      if (token) {
        api.setToken(token.replace('Bearer ', ''));
      }

      // The merchant products endpoint returns both Products and Services
      // (services share the products table; differentiated by `type`).
      const productsRes = await api.getMerchantProducts();
      if (productsRes.success && productsRes.data) {
        const data: any = productsRes.data;
        const list: Product[] = Array.isArray(data) ? data : data.products || [];
        setItems(list);
      }

      const name = await AsyncStorage.getItem('username');
      if (name) setMerchantName(name);
    } catch (error) {
      console.error('Dashboard load error:', error);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadDashboard();
      // Refresh PM status on focus so the CTA reflects a just-completed
      // activation (returning from the activate-pm screen) without a remount.
      reloadPmStatus();
    }, [loadDashboard, reloadPmStatus])
  );

  const onRefresh = () => {
    setRefreshing(true);
    loadDashboard();
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading dashboard..." />
        </View>
      </SafeAreaView>
    );
  }

  // Split products vs services by `type`. The products endpoint returns both
  // because services live in the same table — we partition client-side.
  const products = items.filter((p) => p.type !== 'Service');
  const services = items.filter((p) => p.type === 'Service');

  const totalProducts = products.length;
  const activeProducts = products.filter((p) => p.status === 'active').length;
  const outOfStock = products.filter((p) => p.stock === 0).length;
  const totalServices = services.length;
  const activeServices = services.filter((p) => p.status === 'active').length;

  const recentProducts = products.slice(0, 4);
  const recentServices = services.slice(0, 4);

  // Flatten everything so the SafeAreaView keeps a single FlatList — the
  // dashboard is taller than one screen on most devices, so nesting a
  // ScrollView would forfeit native momentum scrolling.
  const flat: ListItem[] = [{ kind: 'header' }];
  flat.push({ kind: 'sectionTitle', title: 'Recent Products' });
  if (recentProducts.length === 0) {
    flat.push({
      kind: 'emptySection',
      copy: "You haven't added any products yet.",
    });
  } else {
    recentProducts.forEach((p) => flat.push({ kind: 'product', data: p }));
  }
  flat.push({ kind: 'sectionTitle', title: 'Recent Services' });
  if (recentServices.length === 0) {
    flat.push({
      kind: 'emptySection',
      copy: "You haven't listed any services yet.",
    });
  } else {
    recentServices.forEach((s) => flat.push({ kind: 'service', data: s }));
  }

  const renderItem = ({ item }: { item: ListItem }) => {
    if (item.kind === 'header') {
      return (
        <View>
          {/* "Merchant Dashboard" is the header title now — this is the
              greeting, not a second heading. */}
          <View style={{ marginBottom: spacing.lg }}>
            <Text variant="title2" weight="700">
              Welcome back, {merchantName}
            </Text>
          </View>

          {/* Hero card — the merchant overview's headline figure (total listings)
              plus the quick-action grid. Uses the signature AccentCard rail so it
              reads as part of the same product family as the user dashboard. */}
          <AccentCard padding="xl" style={{ marginBottom: spacing.lg }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                justifyContent: 'space-between',
                marginBottom: spacing.sm,
              }}
            >
              <Text variant="caption" color="textMuted">
                Total Listings
              </Text>
              <Badge label="Live" icon="trendingUp" color={colors.accent} background={colors.accentMuted} size="sm" />
            </View>
            <Text variant="display" weight="700">
              {totalProducts + totalServices}
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
              {totalProducts} product{totalProducts === 1 ? '' : 's'} · {totalServices} service{totalServices === 1 ? '' : 's'}
            </Text>

            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-around',
                marginTop: spacing.lg,
                paddingTop: spacing.md,
                borderTopWidth: 1,
                borderTopColor: colors.hairline,
              }}
            >
              <HeroAction
                label="Add Product"
                icon="plus"
                onPress={() => router.push('/merchants/inventory/add' as any)}
              />
              <HeroAction
                label="Add Service"
                icon="construction"
                onPress={() => router.push('/serviceaction?index=1' as any)}
              />
              <HeroAction
                label="Inventory"
                icon="list"
                onPress={() => router.push('/merchants/inventory' as any)}
              />
              <HeroAction
                label="Orders"
                icon="cart"
                onPress={() => router.push('/merchants/orders' as any)}
              />
            </View>
          </AccentCard>

          {/* PM activation CTA — the application was approved but the merchant
              still has to verify identity (BVN/NIN) to mint their NGN account
              and go live. Takes priority over the upgrade CTA. */}
          {!pmLoading && !isPm && pmStatus === 'approved' && (
            <PressableScale
              onPress={() => router.push('/merchants/activate-pm' as any)}
              style={{ marginBottom: spacing.lg }}
            >
              <AccentCard padding="md" railColor={colors.success}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <Icon name="shield" size={24} color={colors.success} />
                  <View style={{ flex: 1 }}>
                    <Text variant="bodyMed" weight="700">
                      Activate your Payment Merchant
                    </Text>
                    <Text variant="caption" color="textSecondary">
                      Approved! Verify your BVN/NIN to go live.
                    </Text>
                  </View>
                  <Icon name="chevronRight" size={20} color={colors.textMuted} />
                </View>
              </AccentCard>
            </PressableScale>
          )}

          {/* PM upgrade CTA — visible only to merchants with no existing PM
              (or a rejected one). Hidden for active (isPm), approved-awaiting-
              activation, and paused PMs — they're already merchants. Absent
              while status is loading. Routes to the locked PM form. */}
          {!pmLoading && !isPm && pmStatus !== 'approved' && pmStatus !== 'paused' && (
            <PressableScale
              onPress={() => router.push('/merchants/merchant-request?type=p2p_merchant' as any)}
              style={{ marginBottom: spacing.lg }}
            >
              <AccentCard padding="md" railColor={colors.info}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <Icon name="storefront" size={24} color={colors.info} />
                  <View style={{ flex: 1 }}>
                    <Text variant="bodyMed" weight="700">
                      Upgrade to Payment Merchant
                    </Text>
                    <Text variant="caption" color="textSecondary">
                      Offer instant P2P buy &amp; sell with NGN settlement.
                    </Text>
                  </View>
                  <Icon name="chevronRight" size={20} color={colors.textMuted} />
                </View>
              </AccentCard>
            </PressableScale>
          )}

          {/* TSA Pay CTA — always visible; the merchant-request gate itself
              routes to the form, or to the pending/approved/rejected status
              screen, based on any existing payment_acceptance request. */}
          <PressableScale
            onPress={() => router.push('/merchants/merchant-request?type=payment_acceptance' as any)}
            style={{ marginBottom: spacing.lg }}
          >
            <AccentCard padding="md" railColor={colors.accent}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="card" size={24} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Accept payments (TSA Pay)
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Let customers pay you in any token, settled to naira or crypto.
                  </Text>
                </View>
                <Icon name="chevronRight" size={20} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>

          {/* Stat tiles — three product KPIs followed by service KPIs. Each tile
              is a Card with a colored top rail keyed to its semantic meaning. */}
          <View style={{ marginBottom: spacing.xs }}>
            <Text variant="overline" color="accent" style={{ marginBottom: spacing.sm }}>
              PRODUCTS
            </Text>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <StatTile label="Total" value={totalProducts} accent={colors.success} />
              <StatTile label="Active" value={activeProducts} accent={colors.info} />
              <StatTile label="Out of stock" value={outOfStock} accent={colors.warning} />
            </View>

            <Text variant="overline" color="accent" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
              SERVICES
            </Text>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <StatTile label="Total" value={totalServices} accent={colors.success} />
              <StatTile label="Active" value={activeServices} accent={colors.info} />
              <StatTile
                label="Inactive"
                value={Math.max(totalServices - activeServices, 0)}
                accent={colors.warning}
              />
            </View>
          </View>
        </View>
      );
    }

    if (item.kind === 'sectionTitle') {
      return (
        <Text variant="title3" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
          {item.title}
        </Text>
      );
    }

    if (item.kind === 'emptySection') {
      return (
        <Card padding="lg" style={{ alignItems: 'center', marginBottom: spacing.sm }}>
          <Text variant="bodySm" color="textMuted">
            {item.copy}
          </Text>
        </Card>
      );
    }

    const p = item.data;
    const isService = item.kind === 'service';
    const heroImage =
      p.images?.find((img: any) => img?.url)?.url ||
      ((p as any).image && typeof (p as any).image === 'string' ? (p as any).image : null);

    return (
      <PressableScale
        onPress={() => {
          if (isService) {
            router.push({
              pathname: '/servicedetail',
              params: {
                id: p.id || p._id || '',
                title: p.name ?? '',
                description: p.description ?? '',
                image: heroImage ?? '',
              },
            });
          } else {
            router.push(`/merchants/inventory/${p.id || p._id}` as any);
          }
        }}
        style={{ marginBottom: spacing.sm }}
      >
        <Card padding="md">
          <View style={{ flexDirection: 'row', alignItems: 'center' }}>
            {heroImage ? (
              <Image
                source={{ uri: heroImage }}
                style={{ width: 44, height: 44, borderRadius: radius.sm, backgroundColor: colors.surfaceAlt }}
              />
            ) : (
              <View
                style={{
                  width: 44,
                  height: 44,
                  borderRadius: radius.sm,
                  backgroundColor: colors.accentMuted,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
              >
                <Icon name={isService ? 'construction' : 'pricetag'} size={20} color={colors.accent} />
              </View>
            )}
            <View style={{ flex: 1, marginLeft: spacing.md }}>
              <Text variant="bodySm" weight="600" numberOfLines={1}>
                {p.name}
              </Text>
              <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 2 }}>
                {isService
                  ? `${p.status || 'draft'} · ${p.location || 'no location'}`
                  : `Stock ${p.stock ?? 0} · ${p.status || 'draft'}`}
              </Text>
            </View>
            <Text variant="bodySm" weight="700" numeric>
              {isService ? 'Contact fee' : formatPrice(Number(p.price ?? 0))}
            </Text>
          </View>
        </Card>
      </PressableScale>
    );
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <FlatList
        data={flat}
        keyExtractor={(item, idx) => `${item.kind}-${idx}`}
        renderItem={renderItem}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
        }
        showsVerticalScrollIndicator={false}
      />
    </SafeAreaView>
  );
}

interface HeroActionProps {
  label: string;
  icon: IconName;
  onPress: () => void;
}
const HeroAction: React.FC<HeroActionProps> = ({ label, icon, onPress }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale onPress={onPress} style={{ alignItems: 'center', flex: 1, paddingHorizontal: spacing.xs }}>
      <View
        style={{
          width: 40,
          height: 40,
          borderRadius: radius.pill,
          backgroundColor: colors.accentMuted,
          justifyContent: 'center',
          alignItems: 'center',
          marginBottom: spacing.xs,
        }}
      >
        <Icon name={icon} size={18} color={colors.accent} />
      </View>
      <Text variant="caption" color="textSecondary" align="center" numberOfLines={1}>
        {label}
      </Text>
    </PressableScale>
  );
};

interface StatTileProps {
  label: string;
  value: number;
  accent: string;
}
const StatTile: React.FC<StatTileProps> = ({ label, value, accent }) => {
  const { spacing } = useTheme();
  return (
    <Card padding="md" style={{ flex: 1, borderTopWidth: 3, borderTopColor: accent }}>
      <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
        {label}
      </Text>
      <Text variant="title2" weight="700" numeric>
        {value}
      </Text>
    </Card>
  );
};
