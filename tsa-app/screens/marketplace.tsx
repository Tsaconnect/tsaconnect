// screens/marketplace.tsx
import { router } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import {
  Alert,
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  TextInput,
  ActivityIndicator,
  RefreshControl,
  Dimensions,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import TradeAndEarnScreen from './TradeAndEarnScreen';
import DepositScreen from './fundfiat';
import AsyncStorage from '@react-native-async-storage/async-storage';
import api from '@/components/services/api';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const GRID_GAP = 10;
const GRID_PADDING = 20;
const TILE_WIDTH = (SCREEN_WIDTH - GRID_PADDING * 2 - GRID_GAP) / 2;

interface CategoryItem {
  id: string;
  title: string;
  description?: string;
  icon: string;
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

const MarketplaceScreen: React.FC = () => {
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
      color: cat.color || '#666',
      productCount: cat.productCount || 0,
      children: cat.children
        ? cat.children.map((child: any) => ({
            id: child.id || child._id,
            title: child.title,
            description: child.description || '',
            icon: ICON_MAP[child.title] || 'label',
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

      if (response.success && response.data) {
        const data = Array.isArray(response.data) ? response.data : [];
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
    router.push(`/subcategory/${cat.id}?categoryId=${cat.id}&categoryTitle=${encodeURIComponent(cat.title)}&subcategoryName=${encodeURIComponent(cat.title)}`);
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
    { key: 'fundfiat', label: 'Buy USDT' },
    { key: 'send', label: 'Sell USDT' },
  ];

  // ── Loading ──
  if (loading && !refreshing && (selectedTab === 'products' || selectedTab === 'services')) {
    return (
      <View style={[styles.container, styles.centered]}>
        <ActivityIndicator size="large" color="#D4AF37" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <ScrollView
        style={styles.scroll}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor="#D4AF37" colors={['#D4AF37']} />
        }
      >
        {/* Search */}
        <View style={styles.searchWrap}>
          <View style={styles.searchBar}>
            <Icon name="search" size={20} color="#999" />
            <TextInput
              style={styles.searchInput}
              placeholder="Search products or services..."
              placeholderTextColor="#AAA"
              value={searchQuery}
              onChangeText={setSearchQuery}
              onSubmitEditing={handleSearch}
              returnKeyType="search"
            />
            {searchQuery.length > 0 && (
              <TouchableOpacity onPress={() => setSearchQuery('')}>
                <Icon name="close" size={18} color="#999" />
              </TouchableOpacity>
            )}
          </View>
        </View>

        {/* Tabs */}
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.tabRow}>
          {TABS.map((tab) => (
            <TouchableOpacity
              key={tab.key}
              style={[styles.tab, selectedTab === tab.key && styles.tabActive]}
              onPress={() => setSelectedTab(tab.key)}
            >
              <Text style={[styles.tabText, selectedTab === tab.key && styles.tabTextActive]}>
                {tab.label}
              </Text>
            </TouchableOpacity>
          ))}
        </ScrollView>

        {/* Content */}
        {(selectedTab === 'products' || selectedTab === 'services') && (
          <View style={styles.content}>
            {error && (
              <TouchableOpacity style={styles.errorBanner} onPress={onRefresh}>
                <Icon name="error-outline" size={16} color="#DC2626" />
                <Text style={styles.errorText}>{error}</Text>
                <Text style={styles.errorRetry}>Tap to retry</Text>
              </TouchableOpacity>
            )}

            {categories.length === 0 && !loading ? (
              <View style={styles.emptyState}>
                <Icon name="category" size={48} color="#CCC" />
                <Text style={styles.emptyText}>No categories found</Text>
              </View>
            ) : (
              <View style={styles.categoryGrid}>
                {categories.map((cat) => (
                  <TouchableOpacity
                    key={cat.id}
                    style={styles.categoryTile}
                    onPress={() => handleCategoryPress(cat)}
                    activeOpacity={0.7}
                  >
                    <View style={[styles.categoryIcon, { backgroundColor: `${cat.color}15` }]}>
                      <Icon name={cat.icon as any} size={26} color={cat.color === '#666666' ? '#D4AF37' : cat.color} />
                    </View>
                    <Text style={styles.categoryTitle} numberOfLines={2}>{cat.title}</Text>
                    {cat.productCount > 0 && (
                      <Text style={styles.categoryCount}>
                        {cat.productCount} {cat.productCount === 1 ? 'item' : 'items'}
                      </Text>
                    )}
                    {cat.children && cat.children.length > 0 && (
                      <Text style={styles.categorySubcount}>
                        {cat.children.length} subcategories
                      </Text>
                    )}
                  </TouchableOpacity>
                ))}
              </View>
            )}

            {/* Quick Access */}
            <Text style={styles.sectionTitle}>Quick Access</Text>
            <View style={styles.quickAccessGrid}>
              {[
                { key: 'featured', label: 'Featured', icon: 'star', color: '#D4AF37', bg: 'rgba(212,175,55,0.12)' },
                { key: 'deals', label: 'Deals', icon: 'local-offer', color: '#16A34A', bg: 'rgba(22,163,74,0.12)' },
                { key: 'trending', label: 'Trending', icon: 'trending-up', color: '#3B82F6', bg: 'rgba(59,130,246,0.12)' },
                { key: 'nearby', label: 'Nearby', icon: 'near-me', color: '#8B5CF6', bg: 'rgba(139,92,246,0.12)' },
              ].map((item) => (
                <TouchableOpacity
                  key={item.key}
                  style={styles.quickAccessTile}
                  onPress={() => router.push(`/subcategory/${item.key}?subcategoryName=${encodeURIComponent(item.label)}&categoryTitle=${encodeURIComponent(item.label)}`)}
                  activeOpacity={0.7}
                >
                  <View style={[styles.quickAccessIcon, { backgroundColor: item.bg }]}>
                    <Icon name={item.icon as any} size={28} color={item.color} />
                  </View>
                  <Text style={styles.quickAccessLabel}>{item.label}</Text>
                </TouchableOpacity>
              ))}
            </View>

            {/* Merchant Banner */}
            {userRole === 'merchant' ? (
              <TouchableOpacity style={styles.merchantBanner} onPress={() => router.push('/merchants/dashboard')} activeOpacity={0.8}>
                <Icon name="storefront" size={28} color="#16A34A" />
                <View style={styles.bannerInfo}>
                  <Text style={styles.bannerTitle}>Merchant Dashboard</Text>
                  <Text style={styles.bannerDesc}>Manage products, orders & inventory</Text>
                </View>
                <Icon name="chevron-right" size={22} color="#16A34A" />
              </TouchableOpacity>
            ) : userRole !== 'admin' && userRole !== 'superadmin' ? (
              <TouchableOpacity style={[styles.merchantBanner, styles.becomeBanner]} onPress={() => router.push('/merchants/merchant-request')} activeOpacity={0.8}>
                <Icon name="store" size={28} color="#D4AF37" />
                <View style={styles.bannerInfo}>
                  <Text style={[styles.bannerTitle, { color: '#8B6914' }]}>Become a Merchant</Text>
                  <Text style={styles.bannerDesc}>Sell your products & services</Text>
                </View>
                <Icon name="chevron-right" size={22} color="#D4AF37" />
              </TouchableOpacity>
            ) : null}
          </View>
        )}

        {selectedTab === 'trade' && <TradeAndEarnScreen />}
        {selectedTab === 'fundfiat' && <DepositScreen />}
        {selectedTab === 'send' && <TradeAndEarnScreen />}

        <View style={{ height: 40 }} />
      </ScrollView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  scroll: { flex: 1 },
  centered: { justifyContent: 'center', alignItems: 'center' },

  // Search
  searchWrap: { backgroundColor: '#FFF', paddingHorizontal: 20, paddingTop: 12, paddingBottom: 16 },
  searchBar: {
    flexDirection: 'row', alignItems: 'center',
    backgroundColor: '#F5F5F5', borderRadius: 12,
    paddingHorizontal: 14, paddingVertical: Platform.OS === 'ios' ? 12 : 8,
  },
  searchInput: { flex: 1, fontSize: 15, color: '#1A1A1A', marginLeft: 10 },

  // Tabs
  tabRow: { paddingHorizontal: 16, paddingVertical: 12, backgroundColor: '#FFF', gap: 6 },
  tab: {
    paddingHorizontal: 16, paddingVertical: 8, borderRadius: 20,
    backgroundColor: '#F5F5F5',
  },
  tabActive: { backgroundColor: '#D4AF37' },
  tabText: { fontSize: 13, fontWeight: '600', color: '#888' },
  tabTextActive: { color: '#FFF' },

  // Content
  content: { padding: GRID_PADDING },

  // Error
  errorBanner: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    backgroundColor: '#FEF2F2', borderRadius: 10, padding: 12, marginBottom: 16,
  },
  errorText: { flex: 1, fontSize: 13, color: '#DC2626' },
  errorRetry: { fontSize: 12, color: '#DC2626', fontWeight: '600' },

  // Empty
  emptyState: { alignItems: 'center', padding: 40 },
  emptyText: { marginTop: 12, fontSize: 15, color: '#AAA' },

  // Category Grid
  categoryGrid: {
    flexDirection: 'row', flexWrap: 'wrap', gap: GRID_GAP,
  },
  categoryTile: {
    width: TILE_WIDTH,
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 16,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  categoryIcon: {
    width: 48, height: 48, borderRadius: 14,
    justifyContent: 'center', alignItems: 'center',
    marginBottom: 12,
  },
  categoryTitle: {
    fontSize: 14, fontWeight: '700', color: '#1A1A1A',
    lineHeight: 18, marginBottom: 4,
  },
  categoryCount: { fontSize: 12, color: '#D4AF37', fontWeight: '500' },
  categorySubcount: { fontSize: 11, color: '#AAA', marginTop: 2 },

  // Quick Access
  sectionTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginTop: 24, marginBottom: 12 },
  quickAccessGrid: { flexDirection: 'row', flexWrap: 'wrap', gap: GRID_GAP },
  quickAccessTile: {
    width: TILE_WIDTH,
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 20,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  quickAccessIcon: {
    width: 56, height: 56, borderRadius: 28,
    justifyContent: 'center', alignItems: 'center',
    marginBottom: 10,
  },
  quickAccessLabel: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },

  // Merchant Banner
  merchantBanner: {
    flexDirection: 'row', alignItems: 'center', gap: 12,
    backgroundColor: '#F0FDF4', borderRadius: 14,
    padding: 18, marginTop: 20,
    borderWidth: 1, borderColor: '#BBF7D0',
  },
  becomeBanner: {
    backgroundColor: 'rgba(212,175,55,0.08)',
    borderColor: 'rgba(212,175,55,0.2)',
  },
  bannerInfo: { flex: 1 },
  bannerTitle: { fontSize: 15, fontWeight: '700', color: '#16A34A', marginBottom: 2 },
  bannerDesc: { fontSize: 12, color: '#888' },
});

export default MarketplaceScreen;
