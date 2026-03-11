// app/screens/FiatDepositScreen.tsx
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

const FiatDepositScreen: React.FC = () => {
  const [selectedMethod, setSelectedMethod] = useState<string>('');
  const [showMethodModal, setShowMethodModal] = useState(false);
  const [showCardModal, setShowCardModal] = useState(false);
  const [showBankModal, setShowBankModal] = useState(false);

  const handleMethodSelect = (method: string) => {
    setSelectedMethod(method);
    setShowMethodModal(true);
  };

  const handleProceed = () => {
    setShowMethodModal(false);
    
    if (selectedMethod === 'tsa') {
      router.push('/services'); // Navigate to TSA CONNECT P2P Merchant
    } else if (selectedMethod === 'card') {
      setShowCardModal(true);
    } else if (selectedMethod === 'bank') {
      setShowBankModal(true);
    }
  };

  // Method Selection Modal
  const MethodModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showMethodModal}
      onRequestClose={() => setShowMethodModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Confirm Selection</Text>
            <TouchableOpacity 
              onPress={() => setShowMethodModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>
          
          <View style={styles.modalBody}>
            <Text style={styles.modalText}>
              {selectedMethod === 'tsa' 
                ? 'You are about to access TSA CONNECT P2P Merchant marketplace.'
                : selectedMethod === 'card'
                ? 'You are about to purchase USDT through our third party payment merchant (5% fee).'
                : 'You are about to purchase USDT through our third party payment merchant (3% fee).'}
            </Text>
            
            <View style={styles.feeInfo}>
              <Icon name="info" size={20} color={GOLD_COLORS.dark} />
              <Text style={styles.feeText}>
                {selectedMethod === 'card' 
                  ? '5% processing fee applies for card payments'
                  : selectedMethod === 'bank'
                  ? '3% processing fee applies for bank transfers'
                  : 'Direct peer-to-peer trading with other users'}
              </Text>
            </View>
          </View>

          <View style={styles.modalFooter}>
            <TouchableOpacity 
              style={styles.cancelButton}
              onPress={() => setShowMethodModal(false)}
              activeOpacity={0.8}
            >
              <Text style={styles.cancelButtonText}>Cancel</Text>
            </TouchableOpacity>
            
            <TouchableOpacity 
              style={styles.proceedButton}
              onPress={handleProceed}
              activeOpacity={0.8}
            >
              <Text style={styles.proceedButtonText}>Proceed</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  // Card Payment Modal
  const CardModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showCardModal}
      onRequestClose={() => setShowCardModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Card Payment</Text>
            <TouchableOpacity 
              onPress={() => setShowCardModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>
          
          <ScrollView style={styles.modalBody}>
            <Text style={styles.sectionTitle}>Payment Details</Text>
            
            <View style={styles.paymentCard}>
              <Text style={styles.paymentNote}>
                Enter your card details on the next page. You will be charged a 5% processing fee.
              </Text>
              
              <View style={styles.paymentDetails}>
                <View style={styles.detailItem}>
                  <Text style={styles.detailLabel}>Payment Method:</Text>
                  <Text style={styles.detailValue}>Credit/Debit Card</Text>
                </View>
                <View style={styles.detailItem}>
                  <Text style={styles.detailLabel}>Processing Fee:</Text>
                  <Text style={styles.detailValue}>5%</Text>
                </View>
                <View style={styles.detailItem}>
                  <Text style={styles.detailLabel}>Processing Time:</Text>
                  <Text style={styles.detailValue}>Instant</Text>
                </View>
                <View style={styles.detailItem}>
                  <Text style={styles.detailLabel}>Security:</Text>
                  <Text style={styles.detailValue}>OTP Verification</Text>
                </View>
              </View>
            </View>
            
            <TouchableOpacity
              style={styles.checkoutButton}
              onPress={() => router.push('/payment/card')}
              activeOpacity={0.8}
            >
              <Text style={styles.checkoutButtonText}>Checkout</Text>
            </TouchableOpacity>
          </ScrollView>
        </View>
      </View>
    </Modal>
  );

  // Bank Transfer Modal
  const BankModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showBankModal}
      onRequestClose={() => setShowBankModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Bank Transfer</Text>
            <TouchableOpacity 
              onPress={() => setShowBankModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>
          
          <ScrollView style={styles.modalBody}>
            <Text style={styles.sectionTitle}>Transfer Details</Text>
            
            <View style={styles.bankCard}>
              <Text style={styles.bankNote}>
                Make a transfer to the account below. You will be charged a 3% processing fee.
              </Text>
              
              <View style={styles.bankDetails}>
                <View style={styles.bankDetailItem}>
                  <Text style={styles.bankDetailLabel}>Bank Name:</Text>
                  <Text style={styles.bankDetailValue}>TSA Connect Bank</Text>
                </View>
                <View style={styles.bankDetailItem}>
                  <Text style={styles.bankDetailLabel}>Account Number:</Text>
                  <Text style={styles.bankDetailValue}>1234567890</Text>
                </View>
                <View style={styles.bankDetailItem}>
                  <Text style={styles.bankDetailLabel}>Account Name:</Text>
                  <Text style={styles.bankDetailValue}>TSA Connect Ltd</Text>
                </View>
                <View style={styles.bankDetailItem}>
                  <Text style={styles.bankDetailLabel}>Processing Fee:</Text>
                  <Text style={styles.bankDetailValue}>3%</Text>
                </View>
                <View style={styles.bankDetailItem}>
                  <Text style={styles.bankDetailLabel}>Supported Currencies:</Text>
                  <Text style={styles.bankDetailValue}>NGN, USD</Text>
                </View>
              </View>
            </View>
            
            <TouchableOpacity
              style={styles.proceedButton}
              onPress={() => router.push('/payment/bank')}
              activeOpacity={0.8}
            >
              <Text style={styles.proceedButtonText}>Proceed to Payment</Text>
            </TouchableOpacity>
          </ScrollView>
        </View>
      </View>
    </Modal>
  );

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
          <Text style={styles.headerTitle}>Fiat Deposit</Text>
        </View>

        {/* Main Content */}
        <View style={styles.content}>
          <Text style={styles.sectionTitle}>Select Deposit Method</Text>
          <Text style={styles.sectionDescription}>
            Choose how you want to deposit fiat currency to purchase USDT
          </Text>

          {/* Method Options */}
          <View style={styles.methodsContainer}>
            {/* TSA CONNECT P2P Merchant */}
            <TouchableOpacity
              style={[
                styles.methodCard,
                selectedMethod === 'tsa' && styles.selectedMethodCard
              ]}
              onPress={() => handleMethodSelect('tsa')}
              activeOpacity={0.8}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="connect-without-contact" size={32} color="#F57C00" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>TSA CONNECT P2P Merchant</Text>
                <Text style={styles.methodDescription}>
                  Peer-to-peer trading with other users
                </Text>
                <View style={styles.methodFeatures}>
                  <View style={styles.feature}>
                    <Icon name="people" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Direct P2P</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="speed" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Fast</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="savings" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Low Fees</Text>
                  </View>
                </View>
              </View>
              {selectedMethod === 'tsa' && (
                <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
              )}
            </TouchableOpacity>

            {/* Card Payment */}
            <TouchableOpacity
              style={[
                styles.methodCard,
                selectedMethod === 'card' && styles.selectedMethodCard
              ]}
              onPress={() => handleMethodSelect('card')}
              activeOpacity={0.8}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="credit-card" size={32} color="#2E7D32" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>Card Payment</Text>
                <Text style={styles.methodDescription}>
                  Purchase USDT with credit/debit card
                </Text>
                <View style={styles.methodFeatures}>
                  <View style={styles.feature}>
                    <Icon name="bolt" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Instant</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="lock" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Secure</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="payments" size={14} color={GOLD_COLORS.dark} />
                    <Text style={[styles.featureText, styles.feeText]}>5% fee</Text>
                  </View>
                </View>
              </View>
              {selectedMethod === 'card' && (
                <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
              )}
            </TouchableOpacity>

            {/* Bank Transfer */}
            <TouchableOpacity
              style={[
                styles.methodCard,
                selectedMethod === 'bank' && styles.selectedMethodCard
              ]}
              onPress={() => handleMethodSelect('bank')}
              activeOpacity={0.8}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="account-balance" size={32} color="#1976D2" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>Bank Transfer</Text>
                <Text style={styles.methodDescription}>
                  Transfer from your bank account
                </Text>
                <View style={styles.methodFeatures}>
                  <View style={styles.feature}>
                    <Icon name="schedule" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>1-3 hours</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="security" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Safe</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="payments" size={14} color={GOLD_COLORS.dark} />
                    <Text style={[styles.featureText, styles.feeText]}>3% fee</Text>
                  </View>
                </View>
              </View>
              {selectedMethod === 'bank' && (
                <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
              )}
            </TouchableOpacity>
          </View>

          {/* Information Section */}
          <View style={styles.infoSection}>
            <Text style={styles.infoTitle}>Important Information</Text>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                All transactions are secured with bank-level encryption
              </Text>
            </View>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                Processing times may vary based on payment method
              </Text>
            </View>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                Contact support if you encounter any issues
              </Text>
            </View>
          </View>
        </View>
      </ScrollView>

      {/* Modals */}
      <MethodModal />
      <CardModal />
      <BankModal />
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
  sectionTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 8,
  },
  sectionDescription: {
    fontSize: 16,
    color: '#666666',
    marginBottom: 24,
  },
  methodsContainer: {
    gap: 16,
    marginBottom: 32,
  },
  methodCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    borderWidth: 2,
    borderColor: 'transparent',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  selectedMethodCard: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  methodIcon: {
    width: 64,
    height: 64,
    borderRadius: 32,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  methodInfo: {
    flex: 1,
  },
  methodTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  methodDescription: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 8,
  },
  methodFeatures: {
    flexDirection: 'row',
    gap: 8,
  },
  feature: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 6,
  },
  featureText: {
    fontSize: 12,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
    marginLeft: 4,
  },
  feeText: {
    color: GOLD_COLORS.error,
  },
  infoSection: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
  },
  infoTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  infoItem: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 8,
  },
  infoText: {
    fontSize: 14,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    lineHeight: 20,
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
  modalText: {
    fontSize: 16,
    color: '#666666',
    marginBottom: 20,
    lineHeight: 24,
  },
  feeInfo: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#FFF8E1',
    padding: 16,
    borderRadius: 12,
  },
  feeText: {
    fontSize: 14,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    lineHeight: 20,
  },
  modalFooter: {
    flexDirection: 'row',
    paddingHorizontal: 24,
    paddingBottom: 24,
    gap: 12,
  },
  cancelButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
  },
  cancelButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  proceedButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
  },
  proceedButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  paymentCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
  },
  paymentNote: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 16,
    lineHeight: 20,
  },
  paymentDetails: {
    gap: 8,
  },
  detailItem: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 8,
    borderBottomWidth: 1,
    borderBottomColor: GOLD_COLORS.muted,
  },
  detailLabel: {
    fontSize: 14,
    color: '#666666',
  },
  detailValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  checkoutButton: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 18,
    alignItems: 'center',
  },
  checkoutButtonText: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  bankCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
  },
  bankNote: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 16,
    lineHeight: 20,
  },
  bankDetails: {
    gap: 12,
  },
  bankDetailItem: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  bankDetailLabel: {
    fontSize: 14,
    color: '#666666',
  },
  bankDetailValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
});

export default FiatDepositScreen;