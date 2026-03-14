// app/screens/FiatWithdrawalScreen.tsx
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
  TextInput,
  Alert,
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

// Mock Nigerian banks
const NIGERIAN_BANKS = [
  'Access Bank',
  'First Bank',
  'GTBank',
  'UBA',
  'Zenith Bank',
  'Stanbic IBTC',
  'Fidelity Bank',
  'Union Bank',
  'Ecobank',
  'Sterling Bank',
  'Wema Bank',
  'FCMB',
  'Polaris Bank',
  'Keystone Bank',
  'Providus Bank',
  'Suntrust Bank',
  'Heritage Bank',
  'Unity Bank',
  'Jaiz Bank',
  'Titan Trust Bank',
];

const FiatWithdrawalScreen: React.FC = () => {
  const [selectedMethod, setSelectedMethod] = useState<string>('');
  const [showMethodModal, setShowMethodModal] = useState(false);
  const [showBankModal, setShowBankModal] = useState(false);
  const [showConfirmModal, setShowConfirmModal] = useState(false);
  const [selectedBank, setSelectedBank] = useState<string>('');
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  const [amount, setAmount] = useState('');
  const [currency, setCurrency] = useState('NGN');
  const [rate, setRate] = useState(1500); // NGN per USD

  const handleMethodSelect = (method: string) => {
    setSelectedMethod(method);
    setShowMethodModal(true);
  };

  const handleProceed = () => {
    setShowMethodModal(false);
    
    if (selectedMethod === 'tsa') {
      router.push('/services'); // Navigate to TSA CONNECT P2P Merchant
    } else if (selectedMethod === 'bank') {
      setShowBankModal(true);
    }
  };

  const handleBankSelect = (bank: string) => {
    setSelectedBank(bank);
    // Simulate account verification
    if (accountNumber.length === 10) {
      setAccountName('John Doe'); // Mock account name
    }
  };

  const verifyAccount = () => {
    if (!selectedBank || !accountNumber) {
      Alert.alert('Error', 'Please select bank and enter account number');
      return;
    }

    if (accountNumber.length !== 10) {
      Alert.alert('Error', 'Account number must be 10 digits');
      return;
    }

    // Simulate API verification
    setTimeout(() => {
      setAccountName('John Doe'); // Mock verification
      Alert.alert(
        'Account Verified',
        `Account Name: John Doe\nBank: ${selectedBank}\nAccount: ${accountNumber}`,
        [{ text: 'OK' }]
      );
    }, 1000);
  };

  const handleWithdrawal = () => {
    if (!amount || parseFloat(amount) <= 0) {
      Alert.alert('Error', 'Please enter a valid amount');
      return;
    }

    if (!selectedBank || !accountNumber || !accountName) {
      Alert.alert('Error', 'Please verify your bank account details');
      return;
    }

    setShowConfirmModal(true);
  };

  const confirmWithdrawal = () => {
    setShowConfirmModal(false);
    Alert.alert(
      'Withdrawal Processing',
      'Your withdrawal is being processed and you will be credited shortly after confirmation!',
      [
        {
          text: 'View Status',
          onPress: () => router.push('/transactions'),
        },
        {
          text: 'OK',
          onPress: () => {
            // Reset form
            setAmount('');
            setShowBankModal(false);
          },
        },
      ]
    );
  };

  // Calculate total amount in selected currency
  const calculateTotal = () => {
    const usdtAmount = parseFloat(amount) || 0;
    if (currency === 'NGN') {
      return (usdtAmount * rate).toFixed(2);
    }
    return (usdtAmount / rate).toFixed(2);
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
                : 'You are about to sell USDT through our third party payment merchant.'}
            </Text>
            
            <View style={styles.feeInfo}>
              <Icon name="info" size={20} color={GOLD_COLORS.dark} />
              <Text style={styles.feeText}>
                {selectedMethod === 'bank' 
                  ? 'Direct bank withdrawal with verification'
                  : 'Peer-to-peer trading with other users'}
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

  // Bank Withdrawal Modal
  const BankModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showBankModal}
      onRequestClose={() => setShowBankModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={[styles.modalContent, styles.largeModal]}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Bank Withdrawal</Text>
            <TouchableOpacity 
              onPress={() => setShowBankModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>
          
          <ScrollView style={styles.modalBody}>
            <Text style={styles.sectionTitle}>Select Bank</Text>
            
            <View style={styles.bankGrid}>
              {NIGERIAN_BANKS.map((bank) => (
                <TouchableOpacity
                  key={bank}
                  style={[
                    styles.bankOption,
                    selectedBank === bank && styles.selectedBankOption
                  ]}
                  onPress={() => handleBankSelect(bank)}
                  activeOpacity={0.7}
                >
                  <View style={styles.bankIcon}>
                    <Icon name="account-balance" size={20} color={selectedBank === bank ? GOLD_COLORS.primary : '#666'} />
                  </View>
                  <Text style={[
                    styles.bankName,
                    selectedBank === bank && styles.selectedBankName
                  ]}>
                    {bank}
                  </Text>
                  {selectedBank === bank && (
                    <Icon name="check-circle" size={16} color={GOLD_COLORS.primary} />
                  )}
                </TouchableOpacity>
              ))}
            </View>

            {/* Account Details */}
            <View style={styles.formSection}>
              <Text style={styles.formLabel}>Account Number</Text>
              <TextInput
                style={styles.input}
                value={accountNumber}
                onChangeText={setAccountNumber}
                placeholder="Enter 10-digit account number"
                placeholderTextColor="#999"
                keyboardType="number-pad"
                maxLength={10}
              />
              
              <TouchableOpacity
                style={styles.verifyButton}
                onPress={verifyAccount}
                activeOpacity={0.8}
              >
                <Icon name="verified" size={16} color="#FFFFFF" />
                <Text style={styles.verifyButtonText}>Verify Account</Text>
              </TouchableOpacity>
            </View>

            {/* Verified Account Display */}
            {accountName && (
              <View style={styles.verifiedAccount}>
                <Icon name="check-circle" size={24} color={GOLD_COLORS.success} />
                <View style={styles.accountInfo}>
                  <Text style={styles.accountName}>Account Name: {accountName}</Text>
                  <Text style={styles.accountDetails}>
                    {selectedBank} ••••{accountNumber.slice(-4)}
                  </Text>
                </View>
              </View>
            )}

            {/* Amount Selection */}
            <View style={styles.formSection}>
              <Text style={styles.formLabel}>Amount (USDT)</Text>
              <TextInput
                style={styles.input}
                value={amount}
                onChangeText={setAmount}
                placeholder="0.00"
                placeholderTextColor="#999"
                keyboardType="decimal-pad"
              />
              
              <View style={styles.currencyOptions}>
                <TouchableOpacity
                  style={[
                    styles.currencyOption,
                    currency === 'NGN' && styles.selectedCurrencyOption
                  ]}
                  onPress={() => setCurrency('NGN')}
                >
                  <Text style={[
                    styles.currencyText,
                    currency === 'NGN' && styles.selectedCurrencyText
                  ]}>
                    NGN
                  </Text>
                </TouchableOpacity>
                
                <TouchableOpacity
                  style={[
                    styles.currencyOption,
                    currency === 'USD' && styles.selectedCurrencyOption
                  ]}
                  onPress={() => setCurrency('USD')}
                >
                  <Text style={[
                    styles.currencyText,
                    currency === 'USD' && styles.selectedCurrencyText
                  ]}>
                    USD
                  </Text>
                </TouchableOpacity>
              </View>
            </View>

            {/* Rate Display */}
            <View style={styles.rateDisplay}>
              <Text style={styles.rateText}>Current Rate: $1 = #{rate.toLocaleString()}</Text>
            </View>

            {/* Calculation Preview */}
            <View style={styles.calculationCard}>
              <Text style={styles.calculationTitle}>Withdrawal Details</Text>
              
              <View style={styles.calculationRow}>
                <Text style={styles.calculationLabel}>USDT Amount:</Text>
                <Text style={styles.calculationValue}>{amount || '0.00'} USDT</Text>
              </View>
              
              <View style={styles.calculationRow}>
                <Text style={styles.calculationLabel}>Exchange Rate:</Text>
                <Text style={styles.calculationValue}>$1 = #{rate}</Text>
              </View>
              
              <View style={styles.calculationRow}>
                <Text style={styles.calculationLabel}>Total {currency}:</Text>
                <Text style={[styles.calculationValue, styles.totalValue]}>
                  {currency === 'NGN' ? '#' : '$'}{calculateTotal()}
                </Text>
              </View>
              
              <View style={styles.calculationRow}>
                <Text style={styles.calculationLabel}>Processing Fee:</Text>
                <Text style={styles.calculationValue}>
                  {currency === 'NGN' ? '#' : '$'}0.00
                </Text>
              </View>
              
              <View style={[styles.calculationRow, styles.finalAmount]}>
                <Text style={styles.calculationLabel}>You Will Receive:</Text>
                <Text style={[styles.calculationValue, styles.receiveValue]}>
                  {currency === 'NGN' ? '#' : '$'}{calculateTotal()}
                </Text>
              </View>
            </View>

            {/* Withdrawal Button */}
            <TouchableOpacity
              style={[
                styles.withdrawButton,
                (!amount || !selectedBank || !accountName) && styles.disabledButton
              ]}
              onPress={handleWithdrawal}
              disabled={!amount || !selectedBank || !accountName}
              activeOpacity={0.8}
            >
              <Text style={styles.withdrawButtonText}>Approve Withdrawal</Text>
            </TouchableOpacity>
          </ScrollView>
        </View>
      </View>
    </Modal>
  );

  // Confirmation Modal
  const ConfirmModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showConfirmModal}
      onRequestClose={() => setShowConfirmModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Confirm Withdrawal</Text>
            <TouchableOpacity 
              onPress={() => setShowConfirmModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>
          
          <View style={styles.modalBody}>
            <View style={styles.confirmationIcon}>
              <Icon name="check-circle" size={64} color={GOLD_COLORS.success} />
            </View>
            
            <Text style={styles.confirmText}>
              Do you want to continue with withdrawal?
            </Text>
            
            <View style={styles.withdrawalDetails}>
              <Text style={styles.detailLabel}>Amount:</Text>
              <Text style={styles.detailValue}>{amount} USDT</Text>
              
              <Text style={styles.detailLabel}>To Receive:</Text>
              <Text style={styles.detailValue}>
                {currency === 'NGN' ? '#' : '$'}{calculateTotal()} {currency}
              </Text>
              
              <Text style={styles.detailLabel}>Bank:</Text>
              <Text style={styles.detailValue}>{selectedBank}</Text>
              
              <Text style={styles.detailLabel}>Account:</Text>
              <Text style={styles.detailValue}>{accountName}</Text>
            </View>
          </View>

          <View style={styles.modalFooter}>
            <TouchableOpacity 
              style={styles.cancelButton}
              onPress={() => setShowConfirmModal(false)}
              activeOpacity={0.8}
            >
              <Text style={styles.cancelButtonText}>Cancel</Text>
            </TouchableOpacity>
            
            <TouchableOpacity 
              style={styles.confirmButton}
              onPress={confirmWithdrawal}
              activeOpacity={0.8}
            >
              <Text style={styles.confirmButtonText}>Yes, Withdraw</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView style={styles.scrollContainer}>
        {/* Main Content */}
        <View style={styles.content}>
          <Text style={styles.sectionTitle}>Select Withdrawal Method</Text>
          <Text style={styles.sectionDescription}>
            Choose how you want to withdraw fiat currency by selling USDT
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
                  Sell USDT directly to other users
                </Text>
                <View style={styles.methodFeatures}>
                  <View style={styles.feature}>
                    <Icon name="people" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>P2P Trading</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="trending-up" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Better Rates</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="shield" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Secure</Text>
                  </View>
                </View>
              </View>
              {selectedMethod === 'tsa' && (
                <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
              )}
            </TouchableOpacity>

            {/* Bank Withdrawal */}
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
                <Text style={styles.methodTitle}>Bank Withdrawal</Text>
                <Text style={styles.methodDescription}>
                  Direct transfer to your bank account
                </Text>
                <View style={styles.methodFeatures}>
                  <View style={styles.feature}>
                    <Icon name="account-balance" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>All Banks</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="verified" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Verified</Text>
                  </View>
                  <View style={styles.feature}>
                    <Icon name="schedule" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.featureText}>Fast</Text>
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
                Bank withdrawals require account verification
              </Text>
            </View>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                Processing time: 1-3 business days
              </Text>
            </View>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                Minimum withdrawal: 10 USDT
              </Text>
            </View>
            
            <View style={styles.infoItem}>
              <Icon name="info" size={16} color={GOLD_COLORS.dark} />
              <Text style={styles.infoText}>
                Contact support for any withdrawal issues
              </Text>
            </View>
          </View>
        </View>
      </ScrollView>

      {/* Modals */}
      <MethodModal />
      <BankModal />
      <ConfirmModal />
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
  largeModal: {
    maxHeight: '90%',
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
  // Bank Modal Styles
  sectionTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  bankGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    marginBottom: 24,
  },
  bankOption: {
    width: '48%',
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    padding: 12,
    borderRadius: 12,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedBankOption: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  bankIcon: {
    marginRight: 8,
  },
  bankName: {
    fontSize: 14,
    color: '#666666',
    flex: 1,
  },
  selectedBankName: {
    color: '#000000',
    fontWeight: '600',
  },
  formSection: {
    marginBottom: 24,
  },
  formLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 8,
  },
  input: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    fontSize: 16,
    color: '#000000',
    marginBottom: 12,
  },
  verifyButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 12,
  },
  verifyButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  verifiedAccount: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#E8F5E9',
    padding: 16,
    borderRadius: 12,
    marginBottom: 24,
  },
  accountInfo: {
    marginLeft: 12,
    flex: 1,
  },
  accountName: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  accountDetails: {
    fontSize: 14,
    color: '#666666',
  },
  currencyOptions: {
    flexDirection: 'row',
    gap: 12,
  },
  currencyOption: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 12,
    borderRadius: 12,
    alignItems: 'center',
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedCurrencyOption: {
    backgroundColor: GOLD_COLORS.primary,
  },
  currencyText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
  },
  selectedCurrencyText: {
    color: '#000000',
  },
  rateDisplay: {
    backgroundColor: GOLD_COLORS.light,
    padding: 16,
    borderRadius: 12,
    alignItems: 'center',
    marginBottom: 24,
  },
  rateText: {
    fontSize: 16,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
  },
  calculationCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 20,
    marginBottom: 24,
  },
  calculationTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  calculationRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  calculationLabel: {
    fontSize: 14,
    color: '#666666',
  },
  calculationValue: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
  },
  totalValue: {
    fontSize: 18,
    fontWeight: '800',
    color: GOLD_COLORS.dark,
  },
  finalAmount: {
    borderTopWidth: 1,
    borderTopColor: GOLD_COLORS.muted,
    paddingTop: 12,
    marginTop: 4,
  },
  receiveValue: {
    fontSize: 20,
    fontWeight: '900',
    color: GOLD_COLORS.success,
  },
  withdrawButton: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 18,
    alignItems: 'center',
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  disabledButton: {
    opacity: 0.5,
  },
  withdrawButtonText: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  // Confirmation Modal Styles
  confirmationIcon: {
    alignItems: 'center',
    marginBottom: 20,
  },
  confirmText: {
    fontSize: 18,
    fontWeight: '600',
    color: '#000000',
    textAlign: 'center',
    marginBottom: 24,
  },
  withdrawalDetails: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
  },
  detailLabel: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 4,
  },
  detailValue: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 12,
  },
  confirmButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
  },
  confirmButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
});

export default FiatWithdrawalScreen;