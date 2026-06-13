// screens/marketplace.tsx
//
// Marketplace landing — product / service category browse, search, quick-access
// shortcuts, and role-aware merchant banner. Restyled onto the TSA Signature
// design system (theme tokens + @/components/ui primitives). Data fetching
// (api.getCategoryTree / api.getProfile), state, effects, the category
// transform, role gating, and all navigation routes + params are unchanged —
// this is a visual / structural migration only.
import { router } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  ScrollView,
  RefreshControl,
  Dimensions,
  Image,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import TradeAndEarnScreen from './TradeAndEarnScreen';
import AsyncStorage from '@react-native-async-storage/async-storage';
import api from '@/components/services/api';
import CartIconBadge from '@/components/common/CartIconBadge';
import CurrencySelector from '@/components/currency/CurrencySelector';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Card,
  Chip,
  EmptyState,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Text,
  type IconName,
} from '@/components/ui';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const GRID_GAP = 10;
const GRID_PADDING = 16;
const TILE_WIDTH = (SCREEN_WIDTH - GRID_PADDING * 2 - GRID_GAP) / 2;

interface CategoryItem {
  id: string;
  title: string;
  description?: string;
  icon: string;
  image?: string;
  color: string;
  productCount: number;
  children: CategoryItem[];
}

const ICON_MAP: Record<string, string> = {
  'Agricultural & Food Products': 'agriculture',
  'Raw Materials & Natural Resources': 'landslide',
  'Electronics': 'devices',
  'Electronics & Appliances': 'devices',
  'Clothing & Fashion': 'checkroom',
  'Home Goods': 'home',
  'Automotive & Transportation': 'directions-car',
  'Tools & Hardware': 'handyman',
  'Digital & Virtual Products': 'cloud',
  'Professional & Business Services': 'business-center',
  'Personal Services': 'person',
  'Farming & Environmental Services': 'grass',
  'Home & Repair Services': 'home-repair-service',
  'Shoes': 'directions-walk',
  "Men's wears": 'checkroom',
  "Women's wears": 'checkroom',
  'Nuts & Seeds': 'eco',
  'Food & Beverages': 'restaurant',
};

// Quick-access shortcut tiles. Icon names are mapped to the design-system Icon
// registry; tint colors come from the theme palette so they adapt to light/dark.
type QuickColor = 'accent' | 'success' | 'info' | 'escrow';
const QUICK_ACCESS: { key: string; label: string; icon: IconName; tone: QuickColor }[] = [
  { key: 'featured', label: 'Featured', icon: 'star', tone: 'accent' },
  { key: 'deals', label: 'Deals', icon: 'bolt', tone: 'success' },
  { key: 'trending', label: 'Trending', icon: 'trendingUp', tone: 'info' },
  { key: 'nearby', label: 'Nearby', icon: 'send', tone: 'escrow' },
];

const MarketplaceScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedTab, setSelectedTab] = useState<string>('products');
  const [categories, setCategories] = useState<CategoryItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [userRole, setUserRole] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const transformCategories = useCallback((raw: any[]): CategoryItem[] => {
    return raw.map((cat) => ({
      id: cat.id || cat._id,
      title: cat.title,
      description: cat.description || '',
      icon: ICON_MAP[cat.title] || 'category',
      image: typeof cat.image === 'string' && cat.image.trim() !== '' ? cat.image : undefined,
      color: cat.color || '#666',
      productCount: cat.productCount || 0,
      children: cat.children
        ? cat.children.map((child: any) => ({
            id: child.id || child._id,
            title: child.title,
            description: child.description || '',
            icon: ICON_MAP[child.title] || 'label',
            image: typeof child.image === 'string' && child.image.trim() !== '' ? child.image : undefined,
            color: child.color || '#666',
            productCount: child.productCount || 0,
            children: [],
          }))
        : [],
    }));
  }, []);

  const fetchCategories = useCallback(async (type: 'Product' | 'Service') => {
    try {
      setLoading(true);
      setError(null);

      const response = await api.getCategoryTree(type);

      if (response.success) {
        // API may return categories under 'data', 'results', or 'categories'
        const raw = response.data || (response as any).results || (response as any).categories || [];
        const data = Array.isArray(raw) ? raw : [];
        setCategories(transformCategories(data));
      } else {
        throw new Error(response.message || 'Failed to fetch categories');
      }
    } catch (err: any) {
      console.error('Error fetching categories:', err);
      setError(err.message || 'Failed to load categories');
      setCategories([]);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [transformCategories]);

  const refreshUserRole = useCallback(async () => {
    try {
      const token = await api.getStoredToken();
      if (token) {
        api.setToken(token);
        const profileRes = await api.getProfile();
        if (profileRes.success && profileRes.data?.role) {
          const role = profileRes.data.role.toLowerCase();
          setUserRole(role);
          await AsyncStorage.setItem('role', role);
          return;
        }
      }
      const stored = await AsyncStorage.getItem('role');
      setUserRole(stored?.toLowerCase() || null);
    } catch {
      const stored = await AsyncStorage.getItem('role');
      setUserRole(stored?.toLowerCase() || null);
    }
  }, []);

  useEffect(() => { refreshUserRole(); }, [refreshUserRole]);

  useEffect(() => {
    if (selectedTab === 'products') fetchCategories('Product');
    else if (selectedTab === 'services') fetchCategories('Service');
  }, [selectedTab, fetchCategories]);

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    refreshUserRole();
    if (selectedTab === 'products') fetchCategories('Product');
    else if (selectedTab === 'services') fetchCategories('Service');
  }, [selectedTab, fetchCategories, refreshUserRole]);

  const handleCategoryPress = (cat: CategoryItem) => {
    // Always pass categoryId to use getProductsByCategoryTree which correctly filters
    // The getProductsByCategory endpoint ignores subcategoryId and returns all products
    const type = selectedTab === 'services' ? 'Service' : 'Product';
    router.push(`/subcategory/${cat.id}?categoryId=${cat.id}&categoryTitle=${encodeURIComponent(cat.title)}&subcategoryName=${encodeURIComponent(cat.title)}&categoryType=${type}&userRole=${userRole || ''}`);
  };

  const handleSearch = () => {
    if (searchQuery.trim()) {
      router.push(`/subcategory/search?subcategoryName=${encodeURIComponent(searchQuery)}&categoryTitle=${encodeURIComponent('Search Results')}`);
    }
  };

  // ── Tabs ──
  const TABS = [
    { key: 'products', label: 'Products' },
    { key: 'services', label: 'Services' },
    { key: 'trade', label: 'Trade & Earn' },
    { key: 'p2p', label: 'P2P' },
    { key: 'pm', label: 'PM' },
  ];

  // ── Loading ──
  if (loading && !refreshing && (selectedTab === 'products' || selectedTab === 'services')) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
        }
      >
        {/* Search + Currency + Cart */}
        <View
          style={{
            paddingHorizontal: spacing.lg,
            paddingTop: spacing.md,
            paddingBottom: spacing.lg,
            flexDirection: 'row',
            alignItems: 'center',
            gap: spacing.sm,
          }}
        >
          <Input
            containerStyle={{ flex: 1 }}
            placeholder="Search products or services..."
            value={searchQuery}
            onChangeText={setSearchQuery}
            onSubmitEditing={handleSearch}
            returnKeyType="search"
            leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
            rightAdornment={
              searchQuery.length > 0 ? (
                <PressableScale onPress={() => setSearchQuery('')} hitSlop={8}>
                  <Icon name="close" size={16} color={colors.textMuted} />
                </PressableScale>
              ) : undefined
            }
          />
          <CurrencySelector variant="header" />
          <CartIconBadge color={colors.accent} size={24} />
        </View>

        {/* Tabs */}
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, gap: spacing.sm }}
          style={{ marginBottom: spacing.md }}
        >
          {TABS.map((tab) => (
            <Chip
              key={tab.key}
              label={tab.label}
              selected={selectedTab === tab.key}
              onPress={() => setSelectedTab(tab.key)}
            />
          ))}
        </ScrollView>

        {/* Content */}
        {(selectedTab === 'products' || selectedTab === 'services') && (
          <View style={{ paddingHorizontal: spacing.lg }}>
            {error && (
              <PressableScale onPress={onRefresh} style={{ marginBottom: spacing.lg }}>
                <AccentCard padding="md" railColor={colors.danger}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                    <Icon name="warning" size={18} color={colors.danger} />
                    <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                      {error}
                    </Text>
                    <Text variant="bodySm" weight="700" color="danger">
                      Tap to retry
                    </Text>
                  </View>
                </AccentCard>
              </PressableScale>
            )}

            {categories.length === 0 && !loading ? (
              <EmptyState
                icon="list"
                title="No categories found"
                subtitle="Pull to refresh or try another tab."
              />
            ) : (
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: GRID_GAP }}>
                {categories.map((cat) => (
                  <PressableScale
                    key={cat.id}
                    onPress={() => handleCategoryPress(cat)}
                    style={{ width: TILE_WIDTH }}
                  >
                    <Card padding="lg">
                      {cat.image ? (
                        <Image
                          source={{ uri: cat.image }}
                          style={{
                            width: 48,
                            height: 48,
                            borderRadius: radius.md,
                            marginBottom: spacing.md,
                            backgroundColor: colors.surfaceAlt,
                          }}
                          resizeMode="cover"
                        />
                      ) : (
                        <View
                          style={{
                            width: 48,
                            height: 48,
                            borderRadius: radius.md,
                            alignItems: 'center',
                            justifyContent: 'center',
                            marginBottom: spacing.md,
                            backgroundColor: colors.accentMuted,
                          }}
                        >
                          <Icon name="list" size={24} color={colors.accent} />
                        </View>
                      )}
                      <Text variant="bodySm" weight="700" numberOfLines={2}>
                        {cat.title}
                      </Text>
                      {cat.productCount > 0 && (
                        <Text variant="caption" weight="600" color="accent" style={{ marginTop: spacing.xs }}>
                          {cat.productCount} {cat.productCount === 1 ? 'item' : 'items'}
                        </Text>
                      )}
                      {cat.children && cat.children.length > 0 && (
                        <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                          {cat.children.length} subcategories
                        </Text>
                      )}
                    </Card>
                  </PressableScale>
                ))}
              </View>
            )}

            {/* Quick Access */}
            <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
              Quick Access
            </Text>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: GRID_GAP }}>
              {QUICK_ACCESS.map((item) => (
                <PressableScale
                  key={item.key}
                  onPress={() => router.push(`/subcategory/${item.key}?subcategoryName=${encodeURIComponent(item.label)}&categoryTitle=${encodeURIComponent(item.label)}`)}
                  style={{ width: TILE_WIDTH }}
                >
                  <Card padding="xl" style={{ alignItems: 'center' }}>
                    <View
                      style={{
                        width: 56,
                        height: 56,
                        borderRadius: radius.pill,
                        alignItems: 'center',
                        justifyContent: 'center',
                        marginBottom: spacing.sm,
                        backgroundColor:
                          item.tone === 'accent'
                            ? colors.accentMuted
                            : item.tone === 'success'
                              ? colors.successBg
                              : item.tone === 'info'
                                ? colors.infoBg
                                : colors.escrowBg,
                      }}
                    >
                      <Icon
                        name={item.icon}
                        size={26}
                        color={colors[item.tone]}
                      />
                    </View>
                    <Text variant="bodySm" weight="600">
                      {item.label}
                    </Text>
                  </Card>
                </PressableScale>
              ))}
            </View>

            {/* Merchant Banner */}
            {userRole === 'merchant' ? (
              <PressableScale onPress={() => router.push('/merchants/dashboard')} style={{ marginTop: spacing.xl }}>
                <AccentCard padding="md" railColor={colors.success}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                    <Icon name="storefront" size={26} color={colors.success} />
                    <View style={{ flex: 1 }}>
                      <Text variant="bodyMed" weight="700">
                        Merchant Dashboard
                      </Text>
                      <Text variant="caption" color="textSecondary">
                        Manage products, orders & inventory
                      </Text>
                    </View>
                    <Icon name="chevronRight" size={20} color={colors.textMuted} />
                  </View>
                </AccentCard>
              </PressableScale>
            ) : userRole !== 'admin' && userRole !== 'superadmin' ? (
              <PressableScale onPress={() => router.push('/merchants/merchant-request')} style={{ marginTop: spacing.xl }}>
                <AccentCard padding="md">
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                    <Icon name="storefront" size={26} color={colors.accent} />
                    <View style={{ flex: 1 }}>
                      <Text variant="bodyMed" weight="700" color="accent">
                        Become a Merchant
                      </Text>
                      <Text variant="caption" color="textSecondary">
                        Sell your products & services
                      </Text>
                    </View>
                    <Icon name="chevronRight" size={20} color={colors.textMuted} />
                  </View>
                </AccentCard>
              </PressableScale>
            ) : null}
          </View>
        )}

        {selectedTab === 'trade' && <TradeAndEarnScreen />}

        {selectedTab === 'p2p' && (
          <BuySellSection
            heading="P2P"
            tag="Individual"
            subtitle="Trade crypto peer-to-peer with other individuals. Funds are escrowed on-chain until release."
            onBuy={() => router.push('/p2p?side=buy')}
            onSell={() => router.push('/p2p?side=sell')}
          />
        )}

        {selectedTab === 'pm' && (
          <BuySellSection
            heading="PM"
            tag="Corporate"
            subtitle="Trade with corporate market makers for larger, faster fills."
            comingSoon
          />
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

// ── P2P / PM Buy|Sell section ────────────────────────────────────────────────
// A heading ("P2P · Individual" / "PM · Corporate") over two Buy / Sell action
// cards. P2P is live and deep-links into the /p2p marketplace with the right
// side preselected; PM is corporate and not yet implemented (coming-soon).
const BuySellSection: React.FC<{
  heading: string;
  tag: string;
  subtitle: string;
  onBuy?: () => void;
  onSell?: () => void;
  comingSoon?: boolean;
}> = ({ heading, tag, subtitle, onBuy, onSell, comingSoon }) => {
  const { colors, spacing, radius } = useTheme();

  const ActionCard: React.FC<{ side: 'buy' | 'sell'; onPress?: () => void }> = ({ side, onPress }) => {
    const isBuy = side === 'buy';
    const tint = isBuy ? colors.success : colors.danger;
    const tintBg = isBuy ? colors.successBg : colors.dangerBg;
    const card = (
      <Card padding="lg" style={{ flex: 1, opacity: comingSoon ? 0.6 : 1 }}>
        <View
          style={{
            width: 40,
            height: 40,
            borderRadius: radius.md,
            backgroundColor: tintBg,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.sm,
          }}
        >
          <Icon name={isBuy ? 'arrowDown' : 'arrowUp'} size={20} color={tint} />
        </View>
        <Text variant="bodyMed" weight="700">
          {isBuy ? 'Buy' : 'Sell'}
        </Text>
        <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
          {isBuy ? 'Pay fiat, get crypto' : 'Sell crypto for fiat'}
        </Text>
      </Card>
    );
    if (comingSoon || !onPress) return <View style={{ flex: 1 }}>{card}</View>;
    return (
      <PressableScale style={{ flex: 1 }} onPress={onPress}>
        {card}
      </PressableScale>
    );
  };

  return (
    <View style={{ paddingHorizontal: spacing.lg }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.xs }}>
        <Text variant="title2" weight="700">
          {heading}
        </Text>
        <View
          style={{
            paddingHorizontal: spacing.sm,
            paddingVertical: 2,
            borderRadius: radius.pill,
            backgroundColor: colors.surfaceAlt,
          }}
        >
          <Text variant="caption" weight="600" color="textSecondary">
            {tag}
          </Text>
        </View>
        {comingSoon && (
          <View
            style={{
              paddingHorizontal: spacing.sm,
              paddingVertical: 2,
              borderRadius: radius.pill,
              backgroundColor: colors.warningBg,
            }}
          >
            <Text variant="caption" weight="600" style={{ color: colors.warning }}>
              Coming soon
            </Text>
          </View>
        )}
      </View>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
        {subtitle}
      </Text>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <ActionCard side="buy" onPress={onBuy} />
        <ActionCard side="sell" onPress={onSell} />
      </View>
      {comingSoon && (
        <Text variant="caption" color="textMuted" style={{ marginTop: spacing.md, textAlign: 'center' }}>
          PM (corporate) trading isn’t available yet — stay tuned.
        </Text>
      )}
    </View>
  );
};

export default MarketplaceScreen;
