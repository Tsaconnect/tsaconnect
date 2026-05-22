import { router } from 'expo-router';
import React from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  Dimensions,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const GRID_GAP = 10;
const GRID_PADDING = 20;
const TILE_WIDTH = (SCREEN_WIDTH - GRID_PADDING * 2 - GRID_GAP) / 2;

const GOLD = '#D4AF37';
const GOLD_DARK = '#8B6914';

const SERVICE_ITEMS = [
  {
    key: 'p2p',
    title: 'P2P Merchant',
    description: 'Buy & sell USDT peer-to-peer',
    icon: 'swap-horiz',
    color: '#F57C00',
    bg: 'rgba(245,124,0,0.10)',
    route: '/p2p',
  },
  {
    key: 'marketplace',
    title: 'Marketplace',
    description: 'Browse products by category',
    icon: 'storefront',
    color: '#2E7D32',
    bg: 'rgba(46,125,50,0.10)',
    route: '/products',
  },
  {
    key: 'browse-services',
    title: 'Browse Services',
    description: 'Find vendors by category',
    icon: 'business-center',
    color: '#1976D2',
    bg: 'rgba(25,118,210,0.10)',
    route: '/service',
  },
  {
    key: 'register-product',
    title: 'List Product',
    description: 'Register a product to sell',
    icon: 'add-box',
    color: '#7B1FA2',
    bg: 'rgba(123,31,162,0.10)',
    route: '/serviceaction?index=0',
  },
  {
    key: 'register-service',
    title: 'List Service',
    description: 'Register a service to offer',
    icon: 'post-add',
    color: '#00838F',
    bg: 'rgba(0,131,143,0.10)',
    route: '/serviceaction?index=1',
  },
];

const QUICK_ACTIONS = [
  { key: 'deposit', label: 'Deposit', icon: 'account-balance-wallet', color: '#16A34A', bg: 'rgba(22,163,74,0.12)', route: '/p2p' },
  { key: 'my-listings', label: 'My Listings', icon: 'inventory', color: '#3B82F6', bg: 'rgba(59,130,246,0.12)', route: '/myAdverts' },
  { key: 'merchant', label: 'Become Merchant', icon: 'store', color: GOLD, bg: 'rgba(212,175,55,0.12)', route: '/merchants/merchant-request' },
  { key: 'wallet', label: 'Wallet', icon: 'account-balance', color: '#8B5CF6', bg: 'rgba(139,92,246,0.12)', route: '/tokenization' },
];

const ServicesScreen: React.FC = () => {
  return (
    <SafeAreaView style={styles.container}>
      <ScrollView style={styles.scroll} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <View style={styles.header}>
          <View>
            <Text style={styles.headerTitle}>Services</Text>
            <Text style={styles.headerSubtitle}>Explore what TSA Connect offers</Text>
          </View>
        </View>

        {/* Service Grid */}
        <View style={styles.section}>
          <View style={styles.grid}>
            {SERVICE_ITEMS.map((item) => (
              <TouchableOpacity
                key={item.key}
                style={styles.tile}
                //@ts-expect-error
                onPress={() => router.push(item.route)}
                activeOpacity={0.7}
              >
                <View style={[styles.tileIcon, { backgroundColor: item.bg }]}>
                  <Icon name={item.icon as any} size={26} color={item.color} />
                </View>
                <Text style={styles.tileTitle} numberOfLines={1}>{item.title}</Text>
                <Text style={styles.tileDesc} numberOfLines={2}>{item.description}</Text>
              </TouchableOpacity>
            ))}
          </View>
        </View>

        {/* Quick Actions */}
        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Quick Actions</Text>
          <View style={styles.grid}>
            {QUICK_ACTIONS.map((item) => (
              <TouchableOpacity
                key={item.key}
                style={styles.quickTile}
                //@ts-expect-error
                onPress={() => router.push(item.route)}
                activeOpacity={0.7}
              >
                <View style={[styles.quickIcon, { backgroundColor: item.bg }]}>
                  <Icon name={item.icon as any} size={26} color={item.color} />
                </View>
                <Text style={styles.quickLabel}>{item.label}</Text>
              </TouchableOpacity>
            ))}
          </View>
        </View>

        {/* P2P Highlight Banner */}
        <View style={styles.section}>
          <TouchableOpacity
            style={styles.banner}
            onPress={() => router.push('/p2p')}
            activeOpacity={0.8}
          >
            <View style={styles.bannerIconWrap}>
              <Icon name="swap-horiz" size={28} color={GOLD} />
            </View>
            <View style={styles.bannerInfo}>
              <Text style={styles.bannerTitle}>TSA Connect P2P Trading</Text>
              <Text style={styles.bannerDesc}>
                Browse open ads or post your own to trade USDT with verified payment proofs
              </Text>
            </View>
            <Icon name="chevron-right" size={22} color={GOLD} />
          </TouchableOpacity>
        </View>

        <View style={{ height: 40 }} />
      </ScrollView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  scroll: { flex: 1 },

  // Header
  header: {
    backgroundColor: '#FFF',
    paddingHorizontal: GRID_PADDING,
    paddingTop: Platform.OS === 'ios' ? 8 : 16,
    paddingBottom: 20,
  },
  headerTitle: { fontSize: 26, fontWeight: '800', color: '#1A1A1A' },
  headerSubtitle: { fontSize: 14, color: '#999', marginTop: 4 },

  // Sections
  section: { paddingHorizontal: GRID_PADDING, marginTop: 20 },
  sectionTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 12 },

  // Grid
  grid: { flexDirection: 'row', flexWrap: 'wrap', gap: GRID_GAP },

  // Service tiles
  tile: {
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
  tileIcon: {
    width: 48, height: 48, borderRadius: 14,
    justifyContent: 'center', alignItems: 'center',
    marginBottom: 12,
  },
  tileTitle: { fontSize: 14, fontWeight: '700', color: '#1A1A1A', marginBottom: 4 },
  tileDesc: { fontSize: 12, color: '#999', lineHeight: 16 },

  // Quick action tiles
  quickTile: {
    width: TILE_WIDTH,
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 18,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  quickIcon: {
    width: 52, height: 52, borderRadius: 26,
    justifyContent: 'center', alignItems: 'center',
    marginBottom: 10,
  },
  quickLabel: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },

  // Banner
  banner: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(212,175,55,0.08)',
    borderRadius: 14,
    padding: 18,
    borderWidth: 1,
    borderColor: 'rgba(212,175,55,0.20)',
    gap: 12,
  },
  bannerIconWrap: {
    width: 48, height: 48, borderRadius: 24,
    backgroundColor: 'rgba(212,175,55,0.12)',
    justifyContent: 'center', alignItems: 'center',
  },
  bannerInfo: { flex: 1 },
  bannerTitle: { fontSize: 15, fontWeight: '700', color: GOLD_DARK, marginBottom: 2 },
  bannerDesc: { fontSize: 12, color: '#888', lineHeight: 17 },
});

export default ServicesScreen;
