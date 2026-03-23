// app/screens/MarketplaceScreen.tsx
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
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import TradeAndEarnScreen from './TradeAndEarnScreen';
import DepositScreen from './fundfiat';
import AsyncStorage from '@react-native-async-storage/async-storage';
import api, { Category } from '@/components/services/api';

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
};

interface CategoryData {
  id: string;
  title: string;
  icon: string;
  subcategories: string[];
}

interface ApiCategoryData {
  productCategories: CategoryData[];
  serviceCategories: CategoryData[];
}

const MarketplaceScreen: React.FC = () => {
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedCategory, setSelectedCategory] = useState<string>('products');
  const [expandedCategory, setExpandedCategory] = useState<string | null>(null);
  const [selectedSubcategory, setSelectedSubcategory] = useState<string | null>(null);

  // State for API data
  const [productCategories, setProductCategories] = useState<CategoryData[]>([]);
  const [serviceCategories, setServiceCategories] = useState<CategoryData[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [userRole, setUserRole] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Transform backend categories to frontend format
  const transformBackendCategories = useCallback((categories: Category[]): CategoryData[] => {
    // Map backend category type to icon
    const getIconForCategory = (category: Category): string => {
      const iconMap: Record<string, string> = {
        'Agricultural & Food Products': 'agriculture',
        'Raw Materials & Natural Resources': 'landslide',
        'Electronics & Appliances': 'factory',
        'Clothing & Fashion': 'checkroom',
        'Home Goods': 'home',
        'Automotive & Transportation': 'directions-car',
        'Tools & Hardware': 'handyman',
        'Digital & Virtual Products': 'devices',
        'Professional & Business Services': 'business-center',
        'Personal Services': 'person',
        'Farming & Environmental Services': 'grass',
        'Home & Repair Services': 'home-repair-service',
      };

      return iconMap[category.title] || 'category';
    };

    // Create subcategories from children categories or use default
    const getSubcategories = (category: Category): string[] => {
      if (category.children && category.children.length > 0) {
        return category.children.map(child => child.title);
      }

      // If no children, return some default subcategories based on category type
      const defaultSubcategories: Record<string, string[]> = {
        'Agricultural & Food Products': [
          'Grains', 'Vegetables', 'Fruits', 'Meat', 'Poultry', 'Seafood'
        ],
        'Electronics & Appliances': [
          'Mobile Phones & Tablets', 'Computers & Accessories', 'TVs & Home Entertainment'
        ],
        // Add more defaults as needed
      };

      return defaultSubcategories[category.title] || ['Browse Products'];
    };

    return categories.map(category => ({
      id: category._id,
      title: category.title,
      icon: getIconForCategory(category),
      subcategories: getSubcategories(category),
    }));
  }, []);

  // Fetch categories from backend
  const fetchCategories = useCallback(async (type: 'Product' | 'Service') => {
    try {
      setLoading(true);
      setError(null);

      // Fetch categories from backend
      const response = await api.getCategories({
        type,
        active: true,
        parent: 'null' // Get only parent categories initially
      });

      if (response.success) {
        const categories = Array.isArray(response.data) ? response.data : response.data?.categories ?? [];
        const transformedCategories = transformBackendCategories(categories);

        if (type === 'Product') {
          setProductCategories(transformedCategories);
        } else {
          setServiceCategories(transformedCategories);
        }
      } else {
        throw new Error(response.message || 'Failed to fetch categories');
      }
    } catch (err: any) {
      console.error('Error fetching categories:', err);
      setError(err.message || 'Failed to load categories');

      // Fallback to dummy data if API fails
      const fallbackData = type === 'Product' ?
        require('@/data/dummyCategories').PRODUCT_CATEGORIES :
        require('@/data/dummyCategories').SERVICE_CATEGORIES;

      if (type === 'Product') {
        setProductCategories(fallbackData);
      } else {
        setServiceCategories(fallbackData);
      }
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [transformBackendCategories]);

  // Load categories based on selected tab
  // Load user role and refresh it from the server
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

  useEffect(() => {
    refreshUserRole();
  }, [refreshUserRole]);

  useEffect(() => {
    if (selectedCategory === 'products') {
      fetchCategories('Product');
    } else if (selectedCategory === 'services') {
      fetchCategories('Service');
    }
  }, [selectedCategory, fetchCategories]);

  // Handle refresh
  const onRefresh = useCallback(() => {
    setRefreshing(true);
    refreshUserRole();
    if (selectedCategory === 'products') {
      fetchCategories('Product');
    } else if (selectedCategory === 'services') {
      fetchCategories('Service');
    }
  }, [selectedCategory, fetchCategories, refreshUserRole]);

  // Handle category expansion
  const toggleCategory = (categoryId: string) => {
    if (expandedCategory === categoryId) {
      setExpandedCategory(null);
      setSelectedSubcategory(null);
    } else {
      setExpandedCategory(categoryId);
    }
  };

  // Render product category item
  const renderProductCategory = ({ item }: { item: CategoryData }) => {
    const isExpanded = expandedCategory === item.id;

    return (
      <View style={styles.categoryCard}>
        <TouchableOpacity
          style={styles.categoryHeader}
          onPress={() => toggleCategory(item.id)}
          activeOpacity={0.7}
        >
          <View style={styles.categoryIconContainer}>
            <Icon name={item.icon as any} size={24} color={GOLD_COLORS.dark} />
          </View>
          <View style={styles.categoryInfo}>
            <Text style={styles.categoryTitle}>{item.title}</Text>
            <Text style={styles.categoryCount}>
              {item.subcategories.length} subcategories
            </Text>
          </View>
          <Icon
            name={isExpanded ? 'expand-less' : 'expand-more'}
            size={24}
            color={GOLD_COLORS.dark}
          />
        </TouchableOpacity>

        {isExpanded && (
          <View style={styles.subcategoriesContainer}>
            {item.subcategories.map((subcategory: string, index: number) => (
              <TouchableOpacity
                key={`${item.id}-${index}`}
                style={[
                  styles.subcategoryItem,
                  selectedSubcategory === `${item.id}-${subcategory}` && styles.selectedSubcategory
                ]}
                onPress={() => {
                  const subcategoryId = `${item.id}-${subcategory}`;
                  setSelectedSubcategory(subcategoryId);
                  // Navigate to products list for this subcategory
                  router.push({
                    pathname: '/subcategory/[subcategoryId]',
                    params: {
                      subcategoryId,
                      categoryTitle: item.title,
                      subcategoryName: subcategory
                    }
                  });
                }}
                activeOpacity={0.7}
              >
                <Text style={styles.subcategoryText}>{subcategory}</Text>
                <Icon name="chevron-right" size={20} color="#666" />
              </TouchableOpacity>
            ))}
          </View>
        )}
      </View>
    );
  };

  // Render service category item
  const renderServiceCategory = ({ item }: { item: CategoryData }) => {
    const isExpanded = expandedCategory === item.id;

    return (
      <View style={styles.categoryCard}>
        <TouchableOpacity
          style={styles.categoryHeader}
          onPress={() => toggleCategory(item.id)}
          activeOpacity={0.7}
        >
          <View style={styles.categoryIconContainer}>
            <Icon name={item.icon as any} size={24} color={GOLD_COLORS.dark} />
          </View>
          <View style={styles.categoryInfo}>
            <Text style={styles.categoryTitle}>{item.title}</Text>
            <Text style={styles.categoryCount}>
              {item.subcategories.length} services
            </Text>
          </View>
          <Icon
            name={isExpanded ? 'expand-less' : 'expand-more'}
            size={24}
            color={GOLD_COLORS.dark}
          />
        </TouchableOpacity>

        {isExpanded && (
          <View style={styles.subcategoriesContainer}>
            {item.subcategories.map((subcategory: string, index: number) => (
              <TouchableOpacity
                key={`${item.id}-${index}`}
                style={[
                  styles.subcategoryItem,
                  selectedSubcategory === `${item.id}-${subcategory}` && styles.selectedSubcategory
                ]}
                onPress={() => {
                  const subcategoryId = `${item.id}-${subcategory}`;
                  setSelectedSubcategory(subcategoryId);
                  // Navigate to services list for this subcategory
                  router.push({
                    pathname: '/marketplace/services',
                    params: {
                      categoryId: item.id,
                      categoryTitle: item.title,
                      subcategoryName: subcategory
                    }
                  });
                }}
                activeOpacity={0.7}
              >
                <Text style={styles.subcategoryText}>{subcategory}</Text>
                <Icon name="chevron-right" size={20} color="#666" />
              </TouchableOpacity>
            ))}
          </View>
        )}
      </View>
    );
  };

  // Loading state
  if (loading && !refreshing) {
    return (
      <View style={[styles.container, styles.centered]}>
        <ActivityIndicator size="large" color={GOLD_COLORS.primary} />
        <Text style={styles.loadingText}>Loading categories...</Text>
      </View>
    );
  }

  // Error state
  if (error && !loading) {
    return (
      <View style={[styles.container, styles.centered]}>
        <Icon name="error-outline" size={64} color={GOLD_COLORS.error} />
        <Text style={styles.errorText}>{error}</Text>
        <TouchableOpacity
          style={styles.retryButton}
          onPress={() => {
            if (selectedCategory === 'products') {
              fetchCategories('Product');
            } else {
              fetchCategories('Service');
            }
          }}
        >
          <Text style={styles.retryButtonText}>Retry</Text>
        </TouchableOpacity>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <ScrollView
        style={styles.scrollContainer}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={onRefresh}
            colors={[GOLD_COLORS.primary]}
            tintColor={GOLD_COLORS.primary}
          />
        }
      >
        {/* Header */}
        <View style={styles.header}>
          <TouchableOpacity
            onPress={() => router.back()}
            style={styles.backButton}
          >
            <Icon name="arrow-back" size={24} color="#000000" />
          </TouchableOpacity>
          <Text style={styles.headerTitle}>Marketplace</Text>
        </View>

        {/* Search Bar */}
        <View style={styles.searchContainer}>
          <View style={styles.searchBar}>
            <Icon name="search" size={20} color="#666" />
            <TextInput
              style={styles.searchInput}
              placeholder="Search products or services..."
              placeholderTextColor="#999"
              value={searchQuery}
              onChangeText={setSearchQuery}
              onSubmitEditing={() => {
                if (searchQuery.trim()) {
                  router.push({
                    pathname: '/marketplace/search',
                    params: { query: searchQuery }
                  });
                }
              }}
            />
            {searchQuery.length > 0 && (
              <TouchableOpacity onPress={() => setSearchQuery('')}>
                <Icon name="close" size={20} color="#666" />
              </TouchableOpacity>
            )}
          </View>

          {/* Filter Buttons */}
          <View style={styles.filterRow}>
            <TouchableOpacity
              style={[
                styles.filterButton,
                styles.locationButton,
              ]}
              onPress={() => Alert.alert('Coming Soon', 'Location filtering will be available soon.')}
            >
              <Icon name="location-on" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.filterButtonText}>Location</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={[
                styles.filterButton,
                styles.categoryButton,
              ]}
              onPress={() => Alert.alert('Coming Soon', 'All categories view will be available soon.')}
            >
              <Icon name="category" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.filterButtonText}>All Categories</Text>
            </TouchableOpacity>
          </View>
        </View>

        {/* Category Tabs */}
        <View style={styles.tabContainer}>
          <TouchableOpacity
            style={[
              styles.tab,
              selectedCategory === 'products' && styles.activeTab
            ]}
            onPress={() => setSelectedCategory('products')}
          >
            <Text style={[
              styles.tabText,
              selectedCategory === 'products' && styles.activeTabText
            ]}>
              Products
            </Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={[
              styles.tab,
              selectedCategory === 'services' && styles.activeTab
            ]}
            onPress={() => setSelectedCategory('services')}
          >
            <Text style={[
              styles.tabText,
              selectedCategory === 'services' && styles.activeTabText
            ]}>
              Services
            </Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={[
              styles.tab,
              selectedCategory === 'trade' && styles.activeTab
            ]}
            onPress={() => setSelectedCategory('trade')}
          >
            <Text style={styles.tabText}>
              Trade&Earn
            </Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={[
              styles.tab,
              selectedCategory === 'fundfiat' && styles.activeTab
            ]}
            onPress={() => setSelectedCategory('fundfiat')}
          >
            <Text style={styles.tabText}>
              Buy USDT
            </Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={[
              styles.tab,
              selectedCategory === 'send' && styles.activeTab
            ]}
            onPress={() => setSelectedCategory('send')}
          >
            <Text style={styles.tabText}>
              Sell USDT
            </Text>
          </TouchableOpacity>
        </View>

        {/* Sub-tabs for Products */}
        {selectedCategory === 'products' && (
          <View style={styles.subTabContainer}>
            <TouchableOpacity
              style={[
                styles.subTab,
                selectedSubcategory === 'general' && styles.activeSubTab
              ]}
              onPress={() => setSelectedSubcategory('general')}
            >
              <Text style={[
                styles.subTabText,
                selectedSubcategory === 'general' && styles.activeSubTabText
              ]}>
                General Products
              </Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={[
                styles.subTab,
                selectedSubcategory === 'digital' && styles.activeSubTab
              ]}
              onPress={() => setSelectedSubcategory('digital')}
            >
              <Text style={[
                styles.subTabText,
                selectedSubcategory === 'digital' && styles.activeSubTabText
              ]}>
                Digital Products
              </Text>
            </TouchableOpacity>
          </View>
        )}

        {/* Main Content */}
        <View style={styles.content}>
          {selectedCategory === 'products' && (
            <>
              {productCategories.length === 0 ? (
                <View style={styles.emptyState}>
                  <Icon name="category" size={64} color="#999" />
                  <Text style={styles.emptyStateText}>No product categories found</Text>
                </View>
              ) : (
                <View>
                  {productCategories.map((item) => (
                    <View key={item.id}>
                      {renderProductCategory({ item })}
                    </View>
                  ))}
                </View>
              )}
            </>
          )}

          {selectedCategory === 'services' && (
            <>
              {serviceCategories.length === 0 ? (
                <View style={styles.emptyState}>
                  <Icon name="miscellaneous-services" size={64} color="#999" />
                  <Text style={styles.emptyStateText}>No service categories found</Text>
                </View>
              ) : (
                <View>
                  {serviceCategories.map((item) => (
                    <View key={item.id}>
                      {renderServiceCategory({ item })}
                    </View>
                  ))}
                </View>
              )}
            </>
          )}

          {selectedCategory === 'trade' && (
            <TradeAndEarnScreen />
          )}

          {selectedCategory === 'fundfiat' && (
            <DepositScreen />
          )}

          {selectedCategory === 'send' && (
            <TradeAndEarnScreen />
          )}

          {/* Quick Links */}
          {(selectedCategory === 'products' || selectedCategory === 'services') && (
            <View style={styles.quickLinksContainer}>
              <Text style={styles.quickLinksTitle}>Quick Access</Text>

              <View style={styles.quickLinksGrid}>
                <TouchableOpacity
                  style={styles.quickLink}
                  onPress={() => Alert.alert('Coming Soon', 'Featured listings will be available soon.')}
                >
                  <View style={[styles.quickLinkIcon, { backgroundColor: '#FFF3E0' }]}>
                    <Icon name="star" size={24} color="#F57C00" />
                  </View>
                  <Text style={styles.quickLinkText}>Featured</Text>
                </TouchableOpacity>

                <TouchableOpacity
                  style={styles.quickLink}
                  onPress={() => Alert.alert('Coming Soon', 'Deals will be available soon.')}
                >
                  <View style={[styles.quickLinkIcon, { backgroundColor: '#E8F5E9' }]}>
                    <Icon name="local-offer" size={24} color="#2E7D32" />
                  </View>
                  <Text style={styles.quickLinkText}>Deals</Text>
                </TouchableOpacity>

                <TouchableOpacity
                  style={styles.quickLink}
                  onPress={() => Alert.alert('Coming Soon', 'Trending listings will be available soon.')}
                >
                  <View style={[styles.quickLinkIcon, { backgroundColor: '#E3F2FD' }]}>
                    <Icon name="trending-up" size={24} color="#1976D2" />
                  </View>
                  <Text style={styles.quickLinkText}>Trending</Text>
                </TouchableOpacity>

                <TouchableOpacity
                  style={styles.quickLink}
                  onPress={() => Alert.alert('Coming Soon', 'Nearby listings will be available soon.')}
                >
                  <View style={[styles.quickLinkIcon, { backgroundColor: '#F3E5F5' }]}>
                    <Icon name="near-me" size={24} color="#7B1FA2" />
                  </View>
                  <Text style={styles.quickLinkText}>Nearby</Text>
                </TouchableOpacity>
              </View>
            </View>
          )}

          {/* Merchant Banner - role-aware */}
          {(selectedCategory === 'products' || selectedCategory === 'services') && (
            userRole === 'merchant' ? (
              <TouchableOpacity
                style={[styles.merchantBanner, { backgroundColor: '#E8F5E9', borderColor: '#4CAF50' }]}
                onPress={() => router.push('/merchants/dashboard')}
                activeOpacity={0.8}
              >
                <View style={styles.bannerContent}>
                  <Icon name="storefront" size={32} color="#2E7D32" />
                  <View style={styles.bannerText}>
                    <Text style={[styles.bannerTitle, { color: '#2E7D32' }]}>Merchant Dashboard</Text>
                    <Text style={styles.bannerDescription}>
                      Manage your products, orders & inventory
                    </Text>
                  </View>
                  <Icon name="chevron-right" size={24} color="#2E7D32" />
                </View>
              </TouchableOpacity>
            ) : userRole === 'admin' || userRole === 'superadmin' ? null : (
              <TouchableOpacity
                style={styles.merchantBanner}
                onPress={() => router.push('/merchants/merchant-request')}
                activeOpacity={0.8}
              >
                <View style={styles.bannerContent}>
                  <Icon name="store" size={32} color="#000000" />
                  <View style={styles.bannerText}>
                    <Text style={styles.bannerTitle}>Become a Merchant</Text>
                    <Text style={styles.bannerDescription}>
                      Sell your products & services on our platform
                    </Text>
                  </View>
                  <Icon name="chevron-right" size={24} color="#000000" />
                </View>
              </TouchableOpacity>
            )
          )}
        </View>
      </ScrollView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  scrollContainer: {
    flex: 1,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 10 : 20,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  backButton: {
    padding: 8,
    marginRight: 12,
  },
  headerTitle: {
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
  },
  searchContainer: {
    backgroundColor: '#FFFFFF',
    padding: 20,
  },
  searchBar: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 12,
    marginBottom: 16,
  },
  searchInput: {
    flex: 1,
    fontSize: 16,
    color: '#000000',
    marginLeft: 12,
    marginRight: 12,
  },
  filterRow: {
    flexDirection: 'row',
    gap: 12,
  },
  filterButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 16,
    paddingVertical: 10,
    borderRadius: 20,
    flex: 1,
    justifyContent: 'center',
  },
  locationButton: {
    backgroundColor: '#FFF8E1',
  },
  categoryButton: {
    backgroundColor: '#E8F5E9',
  },
  filterButtonText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
    marginLeft: 6,
  },
  tabContainer: {
    flexDirection: 'row',
    backgroundColor: '#FFFFFF',
    paddingHorizontal: 20,
    paddingTop: 12,
  },
  tab: {
    flex: 1,
    paddingVertical: 12,
    alignItems: 'center',
    borderBottomWidth: 2,
    borderBottomColor: 'transparent',
  },
  activeTab: {
    borderBottomColor: GOLD_COLORS.primary,
  },
  tabText: {
    fontSize: 11,
    fontWeight: '600',
    color: '#666666',
  },
  activeTabText: {
    color: '#000000',
    fontWeight: '800',
  },
  subTabContainer: {
    flexDirection: 'row',
    backgroundColor: '#FFFFFF',
    paddingHorizontal: 20,
    paddingTop: 8,
    paddingBottom: 16,
  },
  subTab: {
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 20,
    backgroundColor: GOLD_COLORS.light,
    marginRight: 12,
  },
  activeSubTab: {
    backgroundColor: GOLD_COLORS.primary,
  },
  subTabText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666666',
  },
  activeSubTabText: {
    color: '#000000',
  },
  content: {
    padding: 20,
  },
  categoryCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    marginBottom: 16,
    overflow: 'hidden',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  categoryHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    padding: 20,
  },
  categoryIconContainer: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: GOLD_COLORS.light,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  categoryInfo: {
    flex: 1,
  },
  categoryTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  categoryCount: {
    fontSize: 14,
    color: '#666666',
  },
  subcategoriesContainer: {
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    padding: 16,
  },
  subcategoryItem: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: 12,
    paddingHorizontal: 8,
    borderRadius: 8,
    marginBottom: 4,
  },
  selectedSubcategory: {
    backgroundColor: GOLD_COLORS.light,
  },
  subcategoryText: {
    fontSize: 16,
    color: '#666666',
  },
  quickLinksContainer: {
    marginTop: 24,
    marginBottom: 24,
  },
  quickLinksTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  quickLinksGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 10,
  },
  quickLink: {
    width: '48%',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  quickLinkIcon: {
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 12,
  },
  quickLinkText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    textAlign: 'center',
  },
  merchantBanner: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 16,
    padding: 24,
    marginTop: 20,
  },
  bannerContent: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  bannerText: {
    flex: 1,
    marginLeft: 16,
  },
  bannerTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  bannerDescription: {
    fontSize: 14,
    color: '#000000',
    opacity: 0.8,
  },
  centered: {
    justifyContent: 'center',
    alignItems: 'center',
    padding: 20,
  },
  loadingText: {
    marginTop: 16,
    fontSize: 16,
    color: '#666',
    textAlign: 'center',
  },
  errorText: {
    marginTop: 16,
    fontSize: 16,
    color: GOLD_COLORS.error,
    textAlign: 'center',
    marginBottom: 20,
  },
  retryButton: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 8,
  },
  retryButtonText: {
    color: '#000000',
    fontSize: 16,
    fontWeight: '600',
  },
  emptyState: {
    alignItems: 'center',
    justifyContent: 'center',
    padding: 40,
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
  },
  emptyStateText: {
    marginTop: 16,
    fontSize: 16,
    color: '#999',
    textAlign: 'center',
  },
});

export default MarketplaceScreen;