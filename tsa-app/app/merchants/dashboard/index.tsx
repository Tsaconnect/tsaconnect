import React, { useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  FlatList,
  TouchableOpacity,
  ActivityIndicator,
  RefreshControl,
  Image,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import api, { Product } from '../../../components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useCurrency } from '@/contexts/CurrencyContext';

const GOLD = '#D4AF37';
const GOLD_DARK = '#8B6914';
const HERO_BG = '#1A1A1A';

type ListItem =
  | { kind: 'header' }
  | { kind: 'product'; data: Product }
  | { kind: 'service'; data: Product }
  | { kind: 'sectionTitle'; title: string }
  | { kind: 'emptySection'; copy: string };

export default function MerchantDashboard() {
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [items, setItems] = useState<Product[]>([]);
  const [merchantName, setMerchantName] = useState('Merchant');
  const { formatPrice } = useCurrency();

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
    }, [loadDashboard])
  );

  const onRefresh = () => {
    setRefreshing(true);
    loadDashboard();
  };

  if (loading) {
    return (
      <SafeAreaView style={styles.center}>
        <ActivityIndicator size="large" color={GOLD} />
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
          <View style={styles.header}>
            <Text style={styles.title}>Dashboard</Text>
            <Text style={styles.subtitle}>Welcome back, {merchantName}</Text>
          </View>

          {/* Hero card — matches the user dashboard's BalanceCard vocabulary
              (dark surface, gold accent circles) so the merchant view reads
              as part of the same product family. */}
          <View style={styles.hero}>
            <View style={styles.heroHeader}>
              <Text style={styles.heroLabel}>Total Listings</Text>
              <View style={styles.heroBadge}>
                <Ionicons name="trending-up" size={12} color={GOLD} />
                <Text style={styles.heroBadgeText}>Live</Text>
              </View>
            </View>
            <Text style={styles.heroValue}>{totalProducts + totalServices}</Text>
            <Text style={styles.heroBreakdown}>
              {totalProducts} product{totalProducts === 1 ? '' : 's'} · {totalServices} service{totalServices === 1 ? '' : 's'}
            </Text>

            <View style={styles.heroActions}>
              <HeroAction
                label="Add Product"
                icon="add-circle-outline"
                onPress={() => router.push('/merchants/inventory/add' as any)}
              />
              <HeroAction
                label="Add Service"
                icon="briefcase-outline"
                onPress={() => router.push('/serviceaction?index=1' as any)}
              />
              <HeroAction
                label="Inventory"
                icon="list-outline"
                onPress={() => router.push('/merchants/inventory' as any)}
              />
              <HeroAction
                label="Orders"
                icon="receipt-outline"
                onPress={() => router.push('/merchants/orders' as any)}
              />
            </View>
          </View>

          {/* Stat tiles — three lifting product KPIs followed by service
              KPIs. Visually mirrors the asset-row aesthetic on the user
              dashboard (white card, soft shadow, gold accent). */}
          <View style={styles.statsBlock}>
            <Text style={styles.statsBlockTitle}>Products</Text>
            <View style={styles.statsRow}>
              <StatTile label="Total" value={totalProducts} accent="#16A34A" />
              <StatTile label="Active" value={activeProducts} accent="#2563EB" />
              <StatTile label="Out of stock" value={outOfStock} accent="#F97316" />
            </View>

            <Text style={[styles.statsBlockTitle, { marginTop: 18 }]}>Services</Text>
            <View style={styles.statsRow}>
              <StatTile label="Total" value={totalServices} accent="#16A34A" />
              <StatTile label="Active" value={activeServices} accent="#2563EB" />
              <StatTile
                label="Inactive"
                value={Math.max(totalServices - activeServices, 0)}
                accent="#F97316"
              />
            </View>
          </View>
        </View>
      );
    }

    if (item.kind === 'sectionTitle') {
      return <Text style={styles.sectionTitle}>{item.title}</Text>;
    }

    if (item.kind === 'emptySection') {
      return (
        <View style={styles.emptySection}>
          <Text style={styles.emptySectionText}>{item.copy}</Text>
        </View>
      );
    }

    const p = item.data;
    const isService = item.kind === 'service';
    const heroImage =
      p.images?.find((img: any) => img?.url)?.url ||
      ((p as any).image && typeof (p as any).image === 'string' ? (p as any).image : null);

    return (
      <TouchableOpacity
        style={styles.row}
        activeOpacity={0.7}
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
      >
        {heroImage ? (
          <Image source={{ uri: heroImage }} style={styles.rowThumb} />
        ) : (
          <View style={[styles.rowThumb, styles.rowThumbPlaceholder]}>
            <Ionicons
              name={isService ? 'briefcase-outline' : 'cube-outline'}
              size={22}
              color={GOLD}
            />
          </View>
        )}
        <View style={styles.rowBody}>
          <Text style={styles.rowName} numberOfLines={1}>{p.name}</Text>
          <Text style={styles.rowMeta} numberOfLines={1}>
            {isService
              ? `${p.status || 'draft'} · ${p.location || 'no location'}`
              : `Stock ${p.stock ?? 0} · ${p.status || 'draft'}`}
          </Text>
        </View>
        <Text style={styles.rowPrice}>
          {isService ? 'Contact fee' : formatPrice(Number(p.price ?? 0))}
        </Text>
      </TouchableOpacity>
    );
  };

  return (
    <SafeAreaView style={styles.container}>
      <FlatList
        data={flat}
        keyExtractor={(item, idx) => `${item.kind}-${idx}`}
        renderItem={renderItem}
        contentContainerStyle={styles.listContent}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={GOLD} />
        }
        showsVerticalScrollIndicator={false}
      />
    </SafeAreaView>
  );
}

interface HeroActionProps {
  label: string;
  icon: keyof typeof Ionicons.glyphMap;
  onPress: () => void;
}
const HeroAction: React.FC<HeroActionProps> = ({ label, icon, onPress }) => (
  <TouchableOpacity
    style={styles.heroAction}
    activeOpacity={0.7}
    onPress={onPress}
  >
    <View style={styles.heroActionIcon}>
      <Ionicons name={icon} size={18} color={GOLD} />
    </View>
    <Text style={styles.heroActionLabel} numberOfLines={1}>{label}</Text>
  </TouchableOpacity>
);

interface StatTileProps {
  label: string;
  value: number;
  accent: string;
}
const StatTile: React.FC<StatTileProps> = ({ label, value, accent }) => (
  <View style={[styles.statTile, { borderTopColor: accent }]}>
    <Text style={styles.statTileLabel}>{label}</Text>
    <Text style={styles.statTileValue}>{value}</Text>
  </View>
);

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  center: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#F5F5F7',
  },

  listContent: { paddingBottom: 40 },

  header: { paddingHorizontal: 20, paddingTop: 4, paddingBottom: 12 },
  title: { fontSize: 28, fontWeight: '800', color: '#111827' },
  subtitle: { fontSize: 14, color: '#6B7280', marginTop: 4 },

  hero: {
    marginHorizontal: 16,
    marginTop: 4,
    borderRadius: 16,
    backgroundColor: HERO_BG,
    padding: 20,
  },
  heroHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  heroLabel: { fontSize: 13, color: 'rgba(255,255,255,0.6)' },
  heroBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    backgroundColor: 'rgba(212,175,55,0.15)',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 12,
  },
  heroBadgeText: { fontSize: 11, color: GOLD, fontWeight: '700' },
  heroValue: { fontSize: 32, fontWeight: '800', color: '#FFFFFF' },
  heroBreakdown: {
    fontSize: 13,
    color: 'rgba(255,255,255,0.65)',
    marginTop: 2,
  },
  heroActions: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginTop: 18,
    paddingTop: 14,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255,255,255,0.1)',
  },
  heroAction: { alignItems: 'center', flex: 1, paddingHorizontal: 4 },
  heroActionIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: 'rgba(212,175,55,0.15)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 6,
  },
  heroActionLabel: {
    fontSize: 11,
    color: 'rgba(255,255,255,0.78)',
    fontWeight: '500',
    textAlign: 'center',
  },

  statsBlock: { marginHorizontal: 16, marginTop: 18 },
  statsBlockTitle: {
    fontSize: 13,
    fontWeight: '700',
    color: GOLD_DARK,
    letterSpacing: 0.5,
    textTransform: 'uppercase',
    marginBottom: 8,
  },
  statsRow: { flexDirection: 'row', gap: 10 },
  statTile: {
    flex: 1,
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    paddingVertical: 14,
    paddingHorizontal: 12,
    borderTopWidth: 3,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.05,
    shadowRadius: 3,
    elevation: 1,
  },
  statTileLabel: { fontSize: 11, color: '#6B7280', marginBottom: 4 },
  statTileValue: { fontSize: 22, fontWeight: '800', color: '#111827' },

  sectionTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#111827',
    paddingHorizontal: 20,
    marginTop: 22,
    marginBottom: 10,
  },

  emptySection: {
    marginHorizontal: 16,
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 16,
    alignItems: 'center',
  },
  emptySectionText: { fontSize: 13, color: '#9CA3AF' },

  row: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFFFFF',
    paddingHorizontal: 14,
    paddingVertical: 12,
    marginHorizontal: 16,
    marginBottom: 8,
    borderRadius: 12,
  },
  rowThumb: {
    width: 44,
    height: 44,
    borderRadius: 8,
    backgroundColor: '#F5F5F5',
  },
  rowThumbPlaceholder: { justifyContent: 'center', alignItems: 'center' },
  rowBody: { flex: 1, marginLeft: 12 },
  rowName: { fontSize: 14, fontWeight: '600', color: '#111827' },
  rowMeta: { fontSize: 12, color: '#9CA3AF', marginTop: 2 },
  rowPrice: { fontSize: 14, fontWeight: '700', color: '#111827' },
});
