// app/screens/ServicesScreen.tsx
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
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
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

const ServicesScreen: React.FC = () => {
  const [showTSAModal, setShowTSAModal] = useState(false);
  const [showRegistrationModal, setShowRegistrationModal] = useState(false);
  const [showBuyModal, setShowBuyModal] = useState(false);
  const [showSellModal, setShowSellModal] = useState(false);
  const [registrationType, setRegistrationType] = useState<'internal' | 'external' | null>(null);
  const [selectedCountry, setSelectedCountry] = useState<string>('');
  const [selectedCurrency, setSelectedCurrency] = useState<string>('');
  const [serviceType, setServiceType] = useState<'buy' | 'sell' | ''>('');

  // Mock data
  const countries = ['Nigeria', 'United States', 'United Kingdom', 'Ghana', 'South Africa'];
  const currencies = ['NGN', 'USD', 'GBP', 'GHS', 'ZAR'];

  // TSA CONNECT P2P Merchant Modal
  const TSAModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showTSAModal}
      onRequestClose={() => setShowTSAModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>TSA CONNECT P2P MERCHANT</Text>
            <TouchableOpacity
              onPress={() => setShowTSAModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <ScrollView style={styles.modalBody}>
            {/* Internal Seller Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => {
                setShowTSAModal(false);
                setRegistrationType('internal');
                setShowRegistrationModal(true);
              }}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="store" size={32} color="#2E7D32" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Register as internal seller</Text>
                <Text style={styles.optionDescription}>
                  USDT payment for Fiat
                </Text>
                <View style={styles.optionNote}>
                  <Icon name="info" size={14} color={GOLD_COLORS.dark} />
                  <Text style={styles.noteText}>
                    Buyer (customer/users) must first upload payment proof of which you (merchant) must verify before releasing USDT
                  </Text>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>

            {/* External Buyer Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => {
                setShowTSAModal(false);
                setRegistrationType('external');
                setShowRegistrationModal(true);
              }}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="shopping-cart" size={32} color="#1976D2" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Register as external buyer</Text>
                <Text style={styles.optionDescription}>
                  Fiat payment for USDT
                </Text>
                <View style={styles.optionNote}>
                  <Icon name="info" size={14} color={GOLD_COLORS.dark} />
                  <Text style={styles.noteText}>
                    You (merchant) must first upload payment proof of which seller (customer/users) must verify before releasing USDT
                  </Text>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>
          </ScrollView>

          <TouchableOpacity
            style={styles.cancelButton}
            onPress={() => setShowTSAModal(false)}
            activeOpacity={0.8}
          >
            <Text style={styles.cancelButtonText}>Cancel</Text>
          </TouchableOpacity>
        </View>
      </View>
    </Modal>
  );

  // Merchant Registration Modal
  const RegistrationModal = () => {
    const handleProceed = () => {
      if (registrationType === 'internal') {
        //@ts-expect-error
        router.push('/p2p/buy');
      } else {
        //@ts-expect-error
        router.push('/p2p/sell');
      }
      setShowRegistrationModal(false);
    };

    return (
      <Modal
        animationType="slide"
        transparent={true}
        visible={showRegistrationModal}
        onRequestClose={() => setShowRegistrationModal(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>
                {registrationType === 'internal' ? 'Internal Seller Registration' : 'External Buyer Registration'}
              </Text>
              <TouchableOpacity
                onPress={() => setShowRegistrationModal(false)}
                style={styles.modalCloseButton}
              >
                <Icon name="close" size={24} color="#666" />
              </TouchableOpacity>
            </View>

            <ScrollView style={styles.modalBody}>
              <Text style={styles.modalSubtitle}>
                P2P Trading merchant service sign up page
              </Text>

              {/* Country Selection */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Select country</Text>
                <View style={styles.selectContainer}>
                  {countries.map((country) => (
                    <TouchableOpacity
                      key={country}
                      style={[
                        styles.selectOption,
                        selectedCountry === country && styles.selectedOption
                      ]}
                      onPress={() => setSelectedCountry(country)}
                    >
                      <Text style={[
                        styles.selectOptionText,
                        selectedCountry === country && styles.selectedOptionText
                      ]}>
                        {country}
                      </Text>
                      {selectedCountry === country && (
                        <Icon name="check-circle" size={20} color={GOLD_COLORS.primary} />
                      )}
                    </TouchableOpacity>
                  ))}
                </View>
              </View>

              {/* Currency Selection */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Select your currency</Text>
                <View style={styles.selectContainer}>
                  {currencies.map((currency) => (
                    <TouchableOpacity
                      key={currency}
                      style={[
                        styles.selectOption,
                        selectedCurrency === currency && styles.selectedOption
                      ]}
                      onPress={() => setSelectedCurrency(currency)}
                    >
                      <Text style={[
                        styles.selectOptionText,
                        selectedCurrency === currency && styles.selectedOptionText
                      ]}>
                        {currency}
                      </Text>
                      {selectedCurrency === currency && (
                        <Icon name="check-circle" size={20} color={GOLD_COLORS.primary} />
                      )}
                    </TouchableOpacity>
                  ))}
                </View>
              </View>

              {/* Service Selection */}
              <View style={styles.formSection}>
                <Text style={styles.formLabel}>Select service</Text>
                <View style={styles.serviceOptions}>
                  <TouchableOpacity
                    style={[
                      styles.serviceOption,
                      serviceType === 'buy' && styles.selectedServiceOption
                    ]}
                    onPress={() => setServiceType('buy')}
                  >
                    <Icon
                      name="shopping-cart"
                      size={24}
                      color={serviceType === 'buy' ? GOLD_COLORS.primary : '#666'}
                    />
                    <Text style={[
                      styles.serviceOptionText,
                      serviceType === 'buy' && styles.selectedServiceOptionText
                    ]}>
                      Buy
                    </Text>
                  </TouchableOpacity>

                  <TouchableOpacity
                    style={[
                      styles.serviceOption,
                      serviceType === 'sell' && styles.selectedServiceOption
                    ]}
                    onPress={() => setServiceType('sell')}
                  >
                    <Icon
                      name="store"
                      size={24}
                      color={serviceType === 'sell' ? GOLD_COLORS.primary : '#666'}
                    />
                    <Text style={[
                      styles.serviceOptionText,
                      serviceType === 'sell' && styles.selectedServiceOptionText
                    ]}>
                      Sell
                    </Text>
                  </TouchableOpacity>
                </View>
              </View>

              {/* Information Note */}
              <View style={styles.infoBox}>
                <Icon name="info" size={20} color={GOLD_COLORS.dark} />
                <View style={styles.infoContent}>
                  <Text style={styles.infoTitle}>Important Notes:</Text>
                  <Text style={styles.infoText}>
                    • Funds must match your specified limit range
                    • Good rates attract more users
                    • Set rates with low fees included
                    • Ads remain active until locked funds are exhausted
                  </Text>
                </View>
              </View>
            </ScrollView>

            <View style={styles.modalFooter}>
              <TouchableOpacity
                style={styles.cancelButton}
                onPress={() => setShowRegistrationModal(false)}
                activeOpacity={0.8}
              >
                <Text style={styles.cancelButtonText}>Cancel</Text>
              </TouchableOpacity>

              <TouchableOpacity
                style={[
                  styles.proceedButton,
                  (!selectedCountry || !selectedCurrency || !serviceType) && styles.disabledButton
                ]}
                onPress={handleProceed}
                disabled={!selectedCountry || !selectedCurrency || !serviceType}
                activeOpacity={0.8}
              >
                <Text style={styles.proceedButtonText}>Proceed</Text>
              </TouchableOpacity>
            </View>
          </View>
        </View>
      </Modal>
    );
  };

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView style={styles.scrollContainer}>
        {/* Header */}
        <View style={styles.header}>
          <TouchableOpacity
            onPress={() => router.back()}
            style={styles.backButton}
          >
            <Icon name="arrow-back" size={24} color="#000000" />
          </TouchableOpacity>
          <Text style={styles.headerTitle}>Services</Text>
        </View>

        {/* Main Content */}
        <View style={styles.content}>
          {/* TSA CONNECT P2P Merchant Card */}
          <TouchableOpacity
            style={styles.serviceCard}
            onPress={() => setShowTSAModal(true)}
            activeOpacity={0.8}
          >
            <View style={styles.serviceCardHeader}>
              <View style={[styles.serviceIcon, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="connect-without-contact" size={32} color="#F57C00" />
              </View>
              <View style={styles.serviceCardInfo}>
                <Text style={styles.serviceCardTitle}>TSA CONNECT P2P MERCHANT</Text>
                <Text style={styles.serviceCardDescription}>
                  Peer-to-peer trading platform for merchants
                </Text>
              </View>
              <Icon name="chevron-right" size={24} color={GOLD_COLORS.dark} />
            </View>

            <View style={styles.featuresList}>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Register as internal seller/buyer</Text>
              </View>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Secure payment verification</Text>
              </View>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Competitive rate marketplace</Text>
              </View>
            </View>
          </TouchableOpacity>

          {/* Marketplace Card */}
          <TouchableOpacity
            style={styles.serviceCard}
            //@ts-expect-error
            onPress={() => router.push('/marketplace')}
            activeOpacity={0.8}
          >
            <View style={styles.serviceCardHeader}>
              <View style={[styles.serviceIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="storefront" size={32} color="#2E7D32" />
              </View>
              <View style={styles.serviceCardInfo}>
                <Text style={styles.serviceCardTitle}>Marketplace</Text>
                <Text style={styles.serviceCardDescription}>
                  Buy and sell products & services
                </Text>
              </View>
              <Icon name="chevron-right" size={24} color={GOLD_COLORS.dark} />
            </View>

            <View style={styles.featuresList}>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Products & Services marketplace</Text>
              </View>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Trade & Earn opportunities</Text>
              </View>
              <View style={styles.featureItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.featureText}>Digital products automation</Text>
              </View>
            </View>
          </TouchableOpacity>

          {/* Professional Services Card */}
          <TouchableOpacity
            style={styles.serviceCard}
            //@ts-expect-error
            onPress={() => router.push('/services/professional')}
            activeOpacity={0.8}
          >
            <View style={styles.serviceCardHeader}>
              <View style={[styles.serviceIcon, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="business-center" size={32} color="#1976D2" />
              </View>
              <View style={styles.serviceCardInfo}>
                <Text style={styles.serviceCardTitle}>Professional Services</Text>
                <Text style={styles.serviceCardDescription}>
                  Accounting, Legal, Consulting & more
                </Text>
              </View>
              <Icon name="chevron-right" size={24} color={GOLD_COLORS.dark} />
            </View>
          </TouchableOpacity>

          {/* Farming Services Card */}
          <TouchableOpacity
            style={styles.serviceCard}
            //@ts-expect-error
            onPress={() => router.push('/services/farming')}
            activeOpacity={0.8}
          >
            <View style={styles.serviceCardHeader}>
              <View style={[styles.serviceIcon, { backgroundColor: '#F1F8E9' }]}>
                <Icon name="agriculture" size={32} color="#689F38" />
              </View>
              <View style={styles.serviceCardInfo}>
                <Text style={styles.serviceCardTitle}>Farming Services</Text>
                <Text style={styles.serviceCardDescription}>
                  Irrigation, Landscaping, Environmental consulting
                </Text>
              </View>
              <Icon name="chevron-right" size={24} color={GOLD_COLORS.dark} />
            </View>
          </TouchableOpacity>
        </View>
      </ScrollView>

      {/* Modals */}
      <TSAModal />
      <RegistrationModal />
    </SafeAreaView>
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
  content: {
    padding: 20,
  },
  serviceCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 16,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  serviceCardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  serviceIcon: {
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  serviceCardInfo: {
    flex: 1,
  },
  serviceCardTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  serviceCardDescription: {
    fontSize: 14,
    color: '#666666',
  },
  featuresList: {
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    paddingTop: 16,
  },
  featureItem: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  featureText: {
    fontSize: 14,
    color: '#666666',
    marginLeft: 8,
    flex: 1,
  },
  // Modal Styles
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContent: {
    backgroundColor: '#FFFFFF',
    borderRadius: 20,
    width: '100%',
    maxWidth: 500,
    maxHeight: '80%',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 10 },
    shadowOpacity: 0.25,
    shadowRadius: 20,
    elevation: 10,
  },
  modalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 24,
    paddingVertical: 20,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  modalTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    flex: 1,
  },
  modalCloseButton: {
    padding: 4,
  },
  modalBody: {
    padding: 24,
  },
  modalSubtitle: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
    marginBottom: 24,
    textAlign: 'center',
  },
  optionCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  optionIcon: {
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  optionContent: {
    flex: 1,
  },
  optionTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  optionDescription: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 8,
  },
  optionNote: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#FFF8E1',
    padding: 8,
    borderRadius: 8,
  },
  noteText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    fontStyle: 'italic',
  },
  cancelButton: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
    margin: 24,
    marginTop: 0,
  },
  cancelButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  // Registration Modal Styles
  formSection: {
    marginBottom: 24,
  },
  formLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 12,
  },
  selectContainer: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
  },
  selectOption: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderRadius: 12,
    minWidth: 100,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedOption: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  selectOptionText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666666',
    marginRight: 8,
  },
  selectedOptionText: {
    color: '#000000',
  },
  serviceOptions: {
    flexDirection: 'row',
    gap: 16,
  },
  serviceOption: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 16,
    borderRadius: 12,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedServiceOption: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  serviceOptionText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
    marginLeft: 8,
  },
  selectedServiceOptionText: {
    color: '#000000',
  },
  infoBox: {
    flexDirection: 'row',
    backgroundColor: GOLD_COLORS.light,
    padding: 16,
    borderRadius: 12,
    marginTop: 24,
  },
  infoContent: {
    flex: 1,
    marginLeft: 12,
  },
  infoTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  infoText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    lineHeight: 18,
  },
  modalFooter: {
    flexDirection: 'row',
    paddingHorizontal: 24,
    paddingBottom: 24,
    gap: 12,
  },
  proceedButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
  },
  disabledButton: {
    opacity: 0.5,
  },
  proceedButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
});

export default ServicesScreen;