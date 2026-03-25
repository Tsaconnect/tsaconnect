import { router } from 'expo-router';
import React, { useState } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  Modal,
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
    type: 'modal' as const,
  },
  {
    key: 'marketplace',
    title: 'Marketplace',
    description: 'Browse products & services',
    icon: 'storefront',
    color: '#2E7D32',
    bg: 'rgba(46,125,50,0.10)',
    route: '/products',
  },
  {
    key: 'professional',
    title: 'Professional',
    description: 'Legal, Accounting & more',
    icon: 'business-center',
    color: '#1976D2',
    bg: 'rgba(25,118,210,0.10)',
    route: '/service',
  },
  {
    key: 'farming',
    title: 'Farming',
    description: 'Agriculture & environment',
    icon: 'agriculture',
    color: '#689F38',
    bg: 'rgba(104,159,56,0.10)',
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
  { key: 'deposit', label: 'Deposit', icon: 'account-balance-wallet', color: '#16A34A', bg: 'rgba(22,163,74,0.12)', route: '/sellp2p' },
  { key: 'order', label: 'Order Service', icon: 'receipt-long', color: '#3B82F6', bg: 'rgba(59,130,246,0.12)', route: '/service' },
  { key: 'merchant', label: 'Become Merchant', icon: 'store', color: GOLD, bg: 'rgba(212,175,55,0.12)', route: '/merchants/merchant-request' },
  { key: 'wallet', label: 'Wallet', icon: 'account-balance', color: '#8B5CF6', bg: 'rgba(139,92,246,0.12)', route: '/wallet' },
];

const ServicesScreen: React.FC = () => {
  const [showTSAModal, setShowTSAModal] = useState(false);
  const [showRegistrationModal, setShowRegistrationModal] = useState(false);
  const [registrationType, setRegistrationType] = useState<'internal' | 'external' | null>(null);
  const [selectedCountry, setSelectedCountry] = useState('');
  const [selectedCurrency, setSelectedCurrency] = useState('');
  const [serviceType, setServiceType] = useState<'buy' | 'sell' | ''>('');

  const countries = ['Nigeria', 'United States', 'United Kingdom', 'Ghana', 'South Africa'];
  const currencies = ['NGN', 'USD', 'GBP', 'GHS', 'ZAR'];

  const handleServicePress = (item: typeof SERVICE_ITEMS[0]) => {
    if (item.type === 'modal') {
      setShowTSAModal(true);
    } else if (item.route) {
      //@ts-expect-error
      router.push(item.route);
    }
  };

  const TSAModal = () => (
    <Modal
      animationType="slide"
      transparent
      visible={showTSAModal}
      onRequestClose={() => setShowTSAModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>P2P Merchant</Text>
            <TouchableOpacity onPress={() => setShowTSAModal(false)} style={styles.modalClose}>
              <Icon name="close" size={22} color="#999" />
            </TouchableOpacity>
          </View>

          <ScrollView style={styles.modalBody}>
            <Text style={styles.modalSubtitle}>Choose your merchant role</Text>

            <TouchableOpacity
              style={styles.roleCard}
              onPress={() => {
                setShowTSAModal(false);
                setRegistrationType('internal');
                setShowRegistrationModal(true);
              }}
              activeOpacity={0.7}
            >
              <View style={[styles.roleIcon, { backgroundColor: 'rgba(46,125,50,0.10)' }]}>
                <Icon name="store" size={28} color="#2E7D32" />
              </View>
              <View style={styles.roleInfo}>
                <Text style={styles.roleTitle}>Internal Seller</Text>
                <Text style={styles.roleDesc}>Sell USDT for Fiat currency</Text>
                <View style={styles.roleHint}>
                  <Icon name="info-outline" size={13} color={GOLD_DARK} />
                  <Text style={styles.roleHintText}>
                    Buyer uploads payment proof, you verify before releasing USDT
                  </Text>
                </View>
              </View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.roleCard}
              onPress={() => {
                setShowTSAModal(false);
                setRegistrationType('external');
                setShowRegistrationModal(true);
              }}
              activeOpacity={0.7}
            >
              <View style={[styles.roleIcon, { backgroundColor: 'rgba(25,118,210,0.10)' }]}>
                <Icon name="shopping-cart" size={28} color="#1976D2" />
              </View>
              <View style={styles.roleInfo}>
                <Text style={styles.roleTitle}>External Buyer</Text>
                <Text style={styles.roleDesc}>Buy USDT with Fiat currency</Text>
                <View style={styles.roleHint}>
                  <Icon name="info-outline" size={13} color={GOLD_DARK} />
                  <Text style={styles.roleHintText}>
                    You upload payment proof, seller verifies before releasing USDT
                  </Text>
                </View>
              </View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </TouchableOpacity>
          </ScrollView>

          <TouchableOpacity
            style={styles.modalCancel}
            onPress={() => setShowTSAModal(false)}
            activeOpacity={0.8}
          >
            <Text style={styles.modalCancelText}>Cancel</Text>
          </TouchableOpacity>
        </View>
      </View>
    </Modal>
  );

  const RegistrationModal = () => {
    const handleProceed = () => {
      if (registrationType === 'internal') {
        //@ts-expect-error
        router.push('/sellp2p');
      } else {
        //@ts-expect-error
        router.push('/buyp2p');
      }
      setShowRegistrationModal(false);
    };

    return (
      <Modal
        animationType="slide"
        transparent
        visible={showRegistrationModal}
        onRequestClose={() => setShowRegistrationModal(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>
                {registrationType === 'internal' ? 'Seller Setup' : 'Buyer Setup'}
              </Text>
              <TouchableOpacity
                onPress={() => setShowRegistrationModal(false)}
                style={styles.modalClose}
              >
                <Icon name="close" size={22} color="#999" />
              </TouchableOpacity>
            </View>

            <ScrollView style={styles.modalBody}>
              <Text style={styles.modalSubtitle}>
                Configure your P2P trading preferences
              </Text>

              {/* Country */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Country</Text>
                <View style={styles.chipRow}>
                  {countries.map((c) => (
                    <TouchableOpacity
                      key={c}
                      style={[styles.chip, selectedCountry === c && styles.chipActive]}
                      onPress={() => setSelectedCountry(c)}
                    >
                      <Text style={[styles.chipText, selectedCountry === c && styles.chipTextActive]}>
                        {c}
                      </Text>
                      {selectedCountry === c && <Icon name="check" size={16} color={GOLD} />}
                    </TouchableOpacity>
                  ))}
                </View>
              </View>

              {/* Currency */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Currency</Text>
                <View style={styles.chipRow}>
                  {currencies.map((c) => (
                    <TouchableOpacity
                      key={c}
                      style={[styles.chip, selectedCurrency === c && styles.chipActive]}
                      onPress={() => setSelectedCurrency(c)}
                    >
                      <Text style={[styles.chipText, selectedCurrency === c && styles.chipTextActive]}>
                        {c}
                      </Text>
                      {selectedCurrency === c && <Icon name="check" size={16} color={GOLD} />}
                    </TouchableOpacity>
                  ))}
                </View>
              </View>

              {/* Buy / Sell */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Service type</Text>
                <View style={styles.toggleRow}>
                  <TouchableOpacity
                    style={[styles.toggleBtn, serviceType === 'buy' && styles.toggleActive]}
                    onPress={() => setServiceType('buy')}
                  >
                    <Icon name="shopping-cart" size={20} color={serviceType === 'buy' ? GOLD : '#999'} />
                    <Text style={[styles.toggleText, serviceType === 'buy' && styles.toggleTextActive]}>Buy</Text>
                  </TouchableOpacity>
                  <TouchableOpacity
                    style={[styles.toggleBtn, serviceType === 'sell' && styles.toggleActive]}
                    onPress={() => setServiceType('sell')}
                  >
                    <Icon name="store" size={20} color={serviceType === 'sell' ? GOLD : '#999'} />
                    <Text style={[styles.toggleText, serviceType === 'sell' && styles.toggleTextActive]}>Sell</Text>
                  </TouchableOpacity>
                </View>
              </View>

              {/* Info */}
              <View style={styles.infoBox}>
                <Icon name="lightbulb-outline" size={18} color={GOLD_DARK} />
                <Text style={styles.infoText}>
                  Set competitive rates with low fees to attract more users. Ads stay active until locked funds are exhausted.
                </Text>
              </View>
            </ScrollView>

            <View style={styles.modalFooter}>
              <TouchableOpacity
                style={styles.modalCancel}
                onPress={() => setShowRegistrationModal(false)}
                activeOpacity={0.8}
              >
                <Text style={styles.modalCancelText}>Cancel</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[
                  styles.proceedBtn,
                  (!selectedCountry || !selectedCurrency || !serviceType) && styles.proceedDisabled,
                ]}
                onPress={handleProceed}
                disabled={!selectedCountry || !selectedCurrency || !serviceType}
                activeOpacity={0.8}
              >
                <Text style={styles.proceedText}>Continue</Text>
                <Icon name="arrow-forward" size={18} color="#FFF" />
              </TouchableOpacity>
            </View>
          </View>
        </View>
      </Modal>
    );
  };

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
                onPress={() => handleServicePress(item)}
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
            onPress={() => setShowTSAModal(true)}
            activeOpacity={0.8}
          >
            <View style={styles.bannerIconWrap}>
              <Icon name="swap-horiz" size={28} color={GOLD} />
            </View>
            <View style={styles.bannerInfo}>
              <Text style={styles.bannerTitle}>TSA Connect P2P Trading</Text>
              <Text style={styles.bannerDesc}>
                Register as a merchant to trade USDT with verified payment proofs
              </Text>
            </View>
            <Icon name="chevron-right" size={22} color={GOLD} />
          </TouchableOpacity>
        </View>

        <View style={{ height: 40 }} />
      </ScrollView>

      <TSAModal />
      <RegistrationModal />
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

  // Modal shared
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.45)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContent: {
    backgroundColor: '#FFF',
    borderRadius: 20,
    width: '100%',
    maxWidth: 500,
    maxHeight: '80%',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 8 },
    shadowOpacity: 0.15,
    shadowRadius: 24,
    elevation: 10,
  },
  modalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 24,
    paddingVertical: 18,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', flex: 1 },
  modalClose: { padding: 4 },
  modalBody: { padding: 24 },
  modalSubtitle: { fontSize: 14, color: '#999', marginBottom: 20, textAlign: 'center' },
  modalCancel: {
    backgroundColor: '#F5F5F5',
    borderRadius: 12,
    paddingVertical: 14,
    alignItems: 'center',
    marginHorizontal: 24,
    marginBottom: 24,
  },
  modalCancelText: { fontSize: 15, fontWeight: '600', color: '#666' },
  modalFooter: {
    flexDirection: 'row',
    paddingHorizontal: 24,
    paddingBottom: 24,
    gap: 10,
  },

  // Role cards (TSA modal)
  roleCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FAFAFA',
    borderRadius: 14,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  roleIcon: {
    width: 48, height: 48, borderRadius: 24,
    justifyContent: 'center', alignItems: 'center',
    marginRight: 14,
  },
  roleInfo: { flex: 1 },
  roleTitle: { fontSize: 15, fontWeight: '700', color: '#1A1A1A', marginBottom: 2 },
  roleDesc: { fontSize: 13, color: '#888', marginBottom: 6 },
  roleHint: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: 'rgba(212,175,55,0.08)',
    padding: 8,
    borderRadius: 8,
    gap: 6,
  },
  roleHintText: { fontSize: 11, color: GOLD_DARK, flex: 1, lineHeight: 15 },

  // Registration form
  formSection: { marginBottom: 22 },
  formLabel: { fontSize: 14, fontWeight: '600', color: '#1A1A1A', marginBottom: 10 },
  chipRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F5F5F5',
    paddingHorizontal: 14,
    paddingVertical: 10,
    borderRadius: 10,
    borderWidth: 1.5,
    borderColor: 'transparent',
    gap: 6,
  },
  chipActive: { backgroundColor: 'rgba(212,175,55,0.08)', borderColor: GOLD },
  chipText: { fontSize: 13, fontWeight: '600', color: '#888' },
  chipTextActive: { color: '#1A1A1A' },

  toggleRow: { flexDirection: 'row', gap: 12 },
  toggleBtn: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#F5F5F5',
    paddingVertical: 14,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: 'transparent',
    gap: 8,
  },
  toggleActive: { backgroundColor: 'rgba(212,175,55,0.08)', borderColor: GOLD },
  toggleText: { fontSize: 14, fontWeight: '600', color: '#888' },
  toggleTextActive: { color: '#1A1A1A' },

  infoBox: {
    flexDirection: 'row',
    backgroundColor: 'rgba(212,175,55,0.06)',
    padding: 14,
    borderRadius: 12,
    marginTop: 8,
    gap: 10,
    alignItems: 'flex-start',
  },
  infoText: { fontSize: 12, color: GOLD_DARK, flex: 1, lineHeight: 17 },

  proceedBtn: {
    flex: 1,
    flexDirection: 'row',
    backgroundColor: GOLD,
    borderRadius: 12,
    paddingVertical: 14,
    alignItems: 'center',
    justifyContent: 'center',
    gap: 6,
  },
  proceedDisabled: { opacity: 0.4 },
  proceedText: { fontSize: 15, fontWeight: '700', color: '#FFF' },
});

export default ServicesScreen;
