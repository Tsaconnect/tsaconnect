// WithdrawalScreen.tsx
import { router } from 'expo-router';
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  TextInput,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  ActivityIndicator,
  KeyboardAvoidingView,
  Alert,
  Modal,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
// Types and interfaces
interface BankDetails {
  id: string;
  bankName: string;
  accountNumber: string;
  accountName: string;
  country: string;
  currency: string;
}

interface Merchant {
  id: string;
  name: string;
  rating: number;
  completedTrades: number;
  usdRate: number;
  minAmount: number;
  maxAmount: number;
  responseTime: string;
  paymentMethods: string[];
  verificationLevel: string;
}

interface Transaction {
  id: string;
  merchantId: string;
  amountUSD: number;
  amountLocal: number;
  usdtAmount: number;
  status: 'pending' | 'processing' | 'completed' | 'disputed';
  timestamp: Date;
}

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
  warning: '#FFC107',
  info: '#17A2B8',
};

// Country data
const COUNTRIES = [
  { code: 'US', name: 'United States', currency: 'USD', symbol: '$' },
  { code: 'UK', name: 'United Kingdom', currency: 'GBP', symbol: '£' },
  { code: 'NG', name: 'Nigeria', currency: 'NGN', symbol: '₦' },
  { code: 'GH', name: 'Ghana', currency: 'GHS', symbol: 'GH₵' },
  { code: 'KE', name: 'Kenya', currency: 'KES', symbol: 'KSh' },
  { code: 'ZA', name: 'South Africa', currency: 'ZAR', symbol: 'R' },
  { code: 'IN', name: 'India', currency: 'INR', symbol: '₹' },
  { code: 'CA', name: 'Canada', currency: 'CAD', symbol: 'C$' },
  { code: 'AU', name: 'Australia', currency: 'AUD', symbol: 'A$' },
];

// Merchant Rating Component
const MerchantRating: React.FC<{ rating: number; size?: number }> = ({ rating, size = 16 }) => {
  return (
    <View style={styles.ratingContainer}>
      <Icon name="star" size={size} color="#FFD700" />
      <Text style={[styles.ratingText, { fontSize: size - 2 }]}>{rating.toFixed(1)}</Text>
    </View>
  );
};

// Merchant Card Component
const MerchantCard: React.FC<{
  merchant: Merchant;
  isSelected: boolean;
  onSelect: (merchant: Merchant) => void;
}> = ({ merchant, isSelected, onSelect }) => {
  return (
    <TouchableOpacity
      style={[
        styles.merchantCard,
        isSelected && styles.selectedMerchantCard,
      ]}
      onPress={() => onSelect(merchant)}
      activeOpacity={0.7}
    >
      <View style={styles.merchantHeader}>
        <View style={styles.merchantAvatar}>
          <Text style={styles.merchantAvatarText}>
            {merchant.name.charAt(0).toUpperCase()}
          </Text>
        </View>
        <View style={styles.merchantInfo}>
          <Text style={styles.merchantName}>{merchant.name}</Text>
          <MerchantRating rating={merchant.rating} />
          <Text style={styles.merchantTrades}>
            {merchant.completedTrades.toLocaleString()} trades
          </Text>
        </View>
        <View style={styles.verificationBadge}>
          <Icon
            name="verified"
            size={16}
            color={merchant.verificationLevel === 'high' ? GOLD_COLORS.success : GOLD_COLORS.warning}
          />
          <Text style={styles.verificationText}>
            {merchant.verificationLevel === 'high' ? 'Verified' : 'Basic'}
          </Text>
        </View>
      </View>

      <View style={styles.merchantDetails}>
        <View style={styles.detailRow}>
          <View style={styles.detailItem}>
            <Icon name="attach-money" size={14} color="#666" />
            <Text style={styles.detailText}>
              Rate: ${merchant.usdRate.toFixed(2)}/USDT
            </Text>
          </View>
          <View style={styles.detailItem}>
            <Icon name="schedule" size={14} color="#666" />
            <Text style={styles.detailText}>{merchant.responseTime}</Text>
          </View>
        </View>

        <View style={styles.detailRow}>
          <View style={styles.detailItem}>
            <Icon name="swap-vert" size={14} color="#666" />
            <Text style={styles.detailText}>
              Limit: ${merchant.minAmount}-${merchant.maxAmount}
            </Text>
          </View>
          <View style={styles.detailItem}>
            <Icon name="account-balance" size={14} color="#666" />
            <Text style={styles.detailText}>Bank Transfer</Text>
          </View>
        </View>
      </View>

      {isSelected && (
        <View style={styles.selectedOverlay}>
          <Icon name="check-circle" size={24} color={GOLD_COLORS.success} />
        </View>
      )}
    </TouchableOpacity>
  );
};

// Main WithdrawalScreen Component
const WithdrawalScreen: React.FC = () => {
  // State for withdrawal steps
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [isLoading, setIsLoading] = useState(false);
  const [showSellModal, setShowSellModal] = useState(false);
  const [showConfirmModal, setShowConfirmModal] = useState(false);
  const [showOTPModal, setShowOTPModal] = useState(false);
  const [showBankModal, setShowBankModal] = useState(false);

  // User data
  const [hasBankDetails, setHasBankDetails] = useState(false);
  const [bankDetails, setBankDetails] = useState<BankDetails | null>(null);
  const [selectedCountry, setSelectedCountry] = useState<string>('US');
  const [selectedCurrency, setSelectedCurrency] = useState<string>('USD');

  // Bank form state
  const [bankForm, setBankForm] = useState({
    bankName: '',
    accountNumber: '',
    accountName: '',
  });

  // Transaction data
  const [usdAmount, setUsdAmount] = useState<string>('');
  const [selectedMerchant, setSelectedMerchant] = useState<Merchant | null>(null);
  const [otp, setOtp] = useState<string>('');
  const [transaction, setTransaction] = useState<Transaction | null>(null);

  // Available merchants
  const [merchants, setMerchants] = useState<Merchant[]>([
    {
      id: '1',
      name: 'GoldTrust Exchange',
      rating: 4.9,
      completedTrades: 1245,
      usdRate: 1.00,
      minAmount: 100,
      maxAmount: 5000,
      responseTime: '5-15 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
    },
    {
      id: '2',
      name: 'SecureTrade Pro',
      rating: 4.8,
      completedTrades: 892,
      usdRate: 0.99,
      minAmount: 50,
      maxAmount: 3000,
      responseTime: '10-30 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
    },
    {
      id: '3',
      name: 'QuickCash Merchant',
      rating: 4.7,
      completedTrades: 567,
      usdRate: 1.01,
      minAmount: 20,
      maxAmount: 1000,
      responseTime: 'Instant',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'basic',
    },
    {
      id: '4',
      name: 'Reliable P2P Bank',
      rating: 4.85,
      completedTrades: 2103,
      usdRate: 1.00,
      minAmount: 100,
      maxAmount: 10000,
      responseTime: '15-45 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
    },
  ]);

  // Update currency when country changes
  useEffect(() => {
    const country = COUNTRIES.find(c => c.code === selectedCountry);
    if (country) {
      setSelectedCurrency(country.currency);

      // Update bank details currency if bank exists
      if (bankDetails) {
        setBankDetails({
          ...bankDetails,
          country: selectedCountry,
          currency: country.currency,
        });
      }
    }
  }, [selectedCountry]);

  // Calculate local currency amount
  const calculateLocalAmount = () => {
    const amount = parseFloat(usdAmount) || 0;
    const country = COUNTRIES.find(c => c.code === selectedCountry);
    if (!country || !selectedMerchant) return 0;

    // Simple conversion - in real app, use actual exchange rates
    const rate = selectedMerchant.usdRate;
    return amount * rate;
  };

  // Calculate USDT amount
  const calculateUSDTAmount = () => {
    const amount = parseFloat(usdAmount) || 0;
    return amount; // 1 USDT = $1 (simplified)
  };

  // Handle sell button press
  const handleSellPress = () => {
    setShowSellModal(true);
  };

  // Proceed after understanding sell terms
  const handleProceedSell = () => {
    setShowSellModal(false);
    setStep(1);
  };

  // Handle next step
  const handleNextStep = () => {
    if (step === 1) {
      // Validate step 1
      if (!usdAmount || parseFloat(usdAmount) <= 0) {
        Alert.alert('Error', 'Please enter a valid USD amount');
        return;
      }

      const amount = parseFloat(usdAmount);
      const availableMerchants = merchants.filter(
        m => amount >= m.minAmount && amount <= m.maxAmount
      );

      if (availableMerchants.length === 0) {
        Alert.alert('No Merchants', 'No merchants available for this amount. Please try a different amount.');
        return;
      }

      setStep(2);
    } else if (step === 2) {
      // Validate step 2
      if (!selectedMerchant) {
        Alert.alert('Error', 'Please select a merchant');
        return;
      }

      // Create transaction
      const newTransaction: Transaction = {
        id: Date.now().toString(),
        merchantId: selectedMerchant.id,
        amountUSD: parseFloat(usdAmount),
        amountLocal: calculateLocalAmount(),
        usdtAmount: calculateUSDTAmount(),
        status: 'pending',
        timestamp: new Date(),
      };

      setTransaction(newTransaction);
      setStep(3);
    }
  };

  // Handle release USDT
  const handleReleaseUSDT = () => {
    setShowConfirmModal(true);
  };

  // Confirm payment received
  const handleConfirmPayment = (confirmed: boolean) => {
    setShowConfirmModal(false);

    if (confirmed) {
      setShowOTPModal(true);
    } else {
      Alert.alert(
        'Payment Not Received',
        'Please wait for the merchant to credit you and share payment proof, or use the chat box to communicate with the merchant.',
        [
          { text: 'Chat Box', onPress: () => console.log('Open chat') },
          { text: 'OK', style: 'cancel' },
        ]
      );
    }
  };

  // Handle OTP submission
  const handleOTPSubmit = () => {
    if (otp.length !== 6) {
      Alert.alert('Error', 'Please enter a valid 6-digit OTP');
      return;
    }

    setIsLoading(true);

    // Simulate API call
    setTimeout(() => {
      setIsLoading(false);
      setShowOTPModal(false);

      // Update transaction status
      if (transaction) {
        setTransaction({ ...transaction, status: 'completed' });
      }

      Alert.alert(
        'Success!',
        'USDT has been released to the merchant. Transaction completed successfully.',
        [
          {
            text: 'View Transaction',
            onPress: () => console.log('View transaction'),
          },
          {
            text: 'Done',
            onPress: () => router.back(),
          },
        ]
      );
    }, 2000);
  };

  // Handle report
  const handleReport = () => {
    Alert.alert(
      'Report Dispute',
      'Please describe the issue you\'re experiencing with this transaction.',
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Submit Report', onPress: () => console.log('Submit report') },
      ]
    );
  };

  // Handle chat
  const handleChat = () => {
    console.log('Open chat with merchant');
  };

  // Setup bank details
  const handleSetupBank = () => {
    setBankForm({
      bankName: '',
      accountNumber: '',
      accountName: '',
    });
    setShowBankModal(true);
  };

  // Save bank details
  const handleSaveBankDetails = () => {
    if (!bankForm.bankName || !bankForm.accountNumber || !bankForm.accountName) {
      Alert.alert('Error', 'Please fill in all bank details');
      return;
    }

    const country = COUNTRIES.find(c => c.code === selectedCountry);
    const newBankDetails: BankDetails = {
      id: '1',
      bankName: bankForm.bankName,
      accountNumber: bankForm.accountNumber,
      accountName: bankForm.accountName,
      country: selectedCountry,
      currency: country?.currency || 'USD',
    };

    setBankDetails(newBankDetails);
    setHasBankDetails(true);
    setShowBankModal(false);

    Alert.alert('Success', 'Bank details saved successfully');
  };

  // Edit bank details
  const handleEditBank = () => {
    if (bankDetails) {
      setBankForm({
        bankName: bankDetails.bankName,
        accountNumber: bankDetails.accountNumber,
        accountName: bankDetails.accountName,
      });
      setShowBankModal(true);
    }
  };

  // Sell Terms Modal
  const SellTermsModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showSellModal}
      onRequestClose={() => setShowSellModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Sell USDT</Text>
          </View>

          <ScrollView style={styles.modalBody}>
            <View style={styles.warningBox}>
              <Icon name="warning" size={24} color={GOLD_COLORS.warning} />
              <Text style={styles.warningTitle}>Important Notice</Text>
            </View>

            <Text style={styles.termsText}>
              Dear user, our registered merchant will pay fiat to your bank account
              (following your provided bank account and selected country), and the merchant
              will upload their payment proof 🧾, wait for your verification of their payment
              from your Mobile bank account of which you must be sure that you have seen the
              equivalent amount payment from the merchant on your mobile bank account before
              releasing the USDT to our registered merchant account.
            </Text>

            <View style={styles.merchantNotice}>
              <Icon name="info" size={20} color={GOLD_COLORS.info} />
              <Text style={styles.merchantNoticeText}>
                PS: Dear merchant, user USDT is locked with us. Don't send fake payment or you will be sanctioned!
              </Text>
            </View>

            <View style={styles.securityTips}>
              <Text style={styles.securityTitle}>Security Tips:</Text>
              <View style={styles.tipItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.tipText}>Always verify payment in your bank app</Text>
              </View>
              <View style={styles.tipItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.tipText}>Never release USDT before confirming receipt</Text>
              </View>
              <View style={styles.tipItem}>
                <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                <Text style={styles.tipText}>Use chat box for communication</Text>
              </View>
            </View>
          </ScrollView>

          <View style={styles.modalFooter}>
            <TouchableOpacity
              style={styles.cancelButton}
              onPress={() => setShowSellModal(false)}
            >
              <Text style={styles.cancelButtonText}>Cancel</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.proceedButton}
              onPress={handleProceedSell}
            >
              <Text style={styles.proceedButtonText}>Understood & Proceed</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  // Bank Details Modal
  const BankDetailsModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showBankModal}
      onRequestClose={() => setShowBankModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Setup Bank Details</Text>
            <TouchableOpacity
              onPress={() => setShowBankModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <ScrollView style={styles.modalBody}>
            <View style={styles.bankFormContainer}>
              <Text style={styles.formLabel}>Bank Name</Text>
              <TextInput
                style={styles.formInput}
                value={bankForm.bankName}
                onChangeText={(text) => setBankForm({ ...bankForm, bankName: text })}
                placeholder="e.g., Chase Bank"
                placeholderTextColor="#999"
              />

              <Text style={styles.formLabel}>Account Number</Text>
              <TextInput
                style={styles.formInput}
                value={bankForm.accountNumber}
                onChangeText={(text) => setBankForm({ ...bankForm, accountNumber: text })}
                placeholder="Enter account number"
                placeholderTextColor="#999"
                keyboardType="number-pad"
              />

              <Text style={styles.formLabel}>Account Holder Name</Text>
              <TextInput
                style={styles.formInput}
                value={bankForm.accountName}
                onChangeText={(text) => setBankForm({ ...bankForm, accountName: text })}
                placeholder="Enter full name as on bank account"
                placeholderTextColor="#999"
              />

              <Text style={styles.formLabel}>Country</Text>
              <View style={styles.countryDisplay}>
                <Text style={styles.countryDisplayEmoji}>
                  {getCountryFlag(selectedCountry)}
                </Text>
                <Text style={styles.countryDisplayText}>
                  {COUNTRIES.find(c => c.code === selectedCountry)?.name}
                </Text>
                <Text style={styles.countryCurrencyDisplay}>
                  ({selectedCurrency})
                </Text>
              </View>

              <Text style={styles.formNote}>
                Note: Currency will be automatically set based on selected country
              </Text>
            </View>
          </ScrollView>

          <View style={styles.modalFooter}>
            <TouchableOpacity
              style={styles.cancelButton}
              onPress={() => setShowBankModal(false)}
            >
              <Text style={styles.cancelButtonText}>Cancel</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.saveButton}
              onPress={handleSaveBankDetails}
            >
              <Text style={styles.saveButtonText}>Save Bank Details</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  // Confirm Payment Modal
  const ConfirmPaymentModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showConfirmModal}
      onRequestClose={() => setShowConfirmModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Confirm Payment</Text>
          </View>

          <View style={styles.modalBody}>
            <View style={styles.confirmIcon}>
              <Icon name="account-balance" size={60} color={GOLD_COLORS.primary} />
            </View>

            <Text style={styles.confirmTitle}>
              Confirm you have received cash in your bank
            </Text>

            <Text style={styles.confirmText}>
              Have you verified in your mobile banking app that the payment from the merchant has been received?
            </Text>

            <View style={styles.warningAlert}>
              <Icon name="error" size={20} color={GOLD_COLORS.error} />
              <Text style={styles.warningAlertText}>
                ⚠ Never release USDT until you confirm cash in your bank App ‼
              </Text>
            </View>
          </View>

          <View style={styles.modalFooter}>
            <TouchableOpacity
              style={styles.noButton}
              onPress={() => handleConfirmPayment(false)}
            >
              <Text style={styles.noButtonText}>No</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.yesButton}
              onPress={() => handleConfirmPayment(true)}
            >
              <Text style={styles.yesButtonText}>Yes</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  // OTP Modal
  const OTPModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showOTPModal}
      onRequestClose={() => setShowOTPModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Enter OTP</Text>
            <TouchableOpacity
              onPress={() => setShowOTPModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <View style={styles.modalBody}>
            <View style={styles.otpIcon}>
              <Icon name="lock" size={60} color={GOLD_COLORS.primary} />
            </View>

            <Text style={styles.otpTitle}>Enter OTP to release USDT</Text>

            <Text style={styles.otpText}>
              A 6-digit OTP has been sent to your registered email and phone number
            </Text>

            <TextInput
              style={styles.otpInput}
              value={otp}
              onChangeText={setOtp}
              placeholder="Enter 6-digit OTP"
              placeholderTextColor="#999"
              keyboardType="number-pad"
              maxLength={6}
              autoFocus={true}
            />

            <TouchableOpacity style={styles.resendButton}>
              <Text style={styles.resendText}>Resend OTP (60s)</Text>
            </TouchableOpacity>
          </View>

          <View style={styles.modalFooter}>
            <TouchableOpacity
              style={[styles.submitButton, otp.length !== 6 && styles.submitButtonDisabled]}
              onPress={handleOTPSubmit}
              disabled={otp.length !== 6 || isLoading}
            >
              {isLoading ? (
                <ActivityIndicator size="small" color="#000" />
              ) : (
                <Text style={styles.submitButtonText}>Release USDT</Text>
              )}
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );

  // Render step 1: Setup and amount
  const renderStep1 = () => (
    <View style={styles.stepContainer}>
      <Text style={styles.stepTitle}>Sell USDT for Fiat</Text>

      {/* Bank Details Section */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Bank Details</Text>

        {hasBankDetails && bankDetails ? (
          <View style={styles.bankDetailsCard}>
            <View style={styles.bankHeader}>
              <Icon name="account-balance" size={24} color={GOLD_COLORS.primary} />
              <View style={styles.bankInfoHeader}>
                <Text style={styles.bankName}>{bankDetails.bankName}</Text>
                <View style={styles.bankCountryBadge}>
                  <Text style={styles.bankCountryEmoji}>
                    {getCountryFlag(bankDetails.country)}
                  </Text>
                  <Text style={styles.bankCurrency}>{bankDetails.currency}</Text>
                </View>
              </View>
            </View>

            <View style={styles.bankInfo}>
              <View style={styles.bankInfoRow}>
                <Text style={styles.bankLabel}>Account Number:</Text>
                <Text style={styles.bankValue}>{bankDetails.accountNumber}</Text>
              </View>
              <View style={styles.bankInfoRow}>
                <Text style={styles.bankLabel}>Account Name:</Text>
                <Text style={styles.bankValue}>{bankDetails.accountName}</Text>
              </View>
              <View style={styles.bankInfoRow}>
                <Text style={styles.bankLabel}>Country:</Text>
                <Text style={styles.bankValue}>
                  {COUNTRIES.find(c => c.code === bankDetails.country)?.name}
                </Text>
              </View>
            </View>

            <TouchableOpacity
              style={styles.editBankButton}
              onPress={handleEditBank}
            >
              <Icon name="edit" size={16} color="#000" />
              <Text style={styles.editBankText}>Edit Bank Details</Text>
            </TouchableOpacity>
          </View>
        ) : (
          <TouchableOpacity
            style={styles.setupBankButton}
            onPress={handleSetupBank}
          >
            <Icon name="add-circle" size={24} color={GOLD_COLORS.primary} />
            <Text style={styles.setupBankText}>Setup Bank Details</Text>
          </TouchableOpacity>
        )}
      </View>

      {/* Country Selection */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Select Country</Text>
        <Text style={styles.sectionSubtitle}>Choose country that supports your bank</Text>

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={styles.countryScroll}
        >
          {COUNTRIES.map((country) => (
            <TouchableOpacity
              key={country.code}
              style={[
                styles.countryButton,
                selectedCountry === country.code && styles.selectedCountryButton,
              ]}
              onPress={() => setSelectedCountry(country.code)}
            >
              <Text style={styles.countryEmoji}>
                {getCountryFlag(country.code)}
              </Text>
              <Text style={[
                styles.countryName,
                selectedCountry === country.code && styles.selectedCountryName,
              ]}>
                {country.name}
              </Text>
              <Text style={styles.countryCurrency}>
                {country.currency} {country.symbol}
              </Text>
            </TouchableOpacity>
          ))}
        </ScrollView>
      </View>

      {/* Amount Input */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Enter Amount to Sell</Text>

        <View style={styles.amountInputContainer}>
          <View style={styles.amountInputRow}>
            <Text style={styles.currencySymbol}>$</Text>
            <TextInput
              style={styles.amountInput}
              value={usdAmount}
              onChangeText={setUsdAmount}
              placeholder="0.00"
              placeholderTextColor="#999"
              keyboardType="decimal-pad"
              returnKeyType="done"
            />
            <Text style={styles.currencyText}>USD</Text>
          </View>

          <View style={styles.conversionDisplay}>
            <Text style={styles.conversionLabel}>You will receive:</Text>
            <Text style={styles.conversionAmount}>
              {selectedCurrency} {selectedMerchant ? calculateLocalAmount().toLocaleString() : '0.00'}
            </Text>
          </View>

          <Text style={styles.amountNote}>
            Available balance: 5,000 USDT (≈ $5,000)
          </Text>
        </View>

        {/* Quick Amount Buttons */}
        <View style={styles.quickAmounts}>
          {[100, 500, 1000, 5000].map((amount) => (
            <TouchableOpacity
              key={amount}
              style={styles.quickAmountButton}
              onPress={() => setUsdAmount(amount.toString())}
            >
              <Text style={styles.quickAmountText}>${amount}</Text>
            </TouchableOpacity>
          ))}
        </View>
      </View>
    </View>
  );

  // Render step 2: Select merchant
  const renderStep2 = () => (
    <View style={styles.stepContainer}>
      <Text style={styles.stepTitle}>Select Merchant</Text>
      <Text style={styles.stepSubtitle}>
        Available merchants for ${usdAmount}
      </Text>

      <View style={styles.filterSection}>
        <View style={styles.filterItem}>
          <Icon name="filter-list" size={18} color="#666" />
          <Text style={styles.filterText}>Sort by: Best Rate</Text>
        </View>
        <TouchableOpacity style={styles.filterItem}>
          <Icon name="tune" size={18} color="#666" />
          <Text style={styles.filterText}>Filters</Text>
        </TouchableOpacity>
      </View>

      <ScrollView style={styles.merchantList}>
        {merchants
          .filter(m => {
            const amount = parseFloat(usdAmount) || 0;
            return amount >= m.minAmount && amount <= m.maxAmount;
          })
          .map((merchant) => (
            <MerchantCard
              key={merchant.id}
              merchant={merchant}
              isSelected={selectedMerchant?.id === merchant.id}
              onSelect={setSelectedMerchant}
            />
          ))}
      </ScrollView>

      {/* Selected Merchant Summary */}
      {selectedMerchant && (
        <View style={styles.selectedSummary}>
          <View style={styles.summaryRow}>
            <Text style={styles.summaryLabel}>Selected Rate:</Text>
            <Text style={styles.summaryValue}>
              ${selectedMerchant.usdRate.toFixed(2)}/USDT
            </Text>
          </View>
          <View style={styles.summaryRow}>
            <Text style={styles.summaryLabel}>Payment Method:</Text>
            <Text style={styles.summaryValue}>Bank Transfer</Text>
          </View>
          <View style={styles.summaryRow}>
            <Text style={styles.summaryLabel}>You Will Receive:</Text>
            <Text style={styles.summaryValue}>
              {calculateLocalAmount().toLocaleString()} {selectedCurrency}
            </Text>
          </View>
        </View>
      )}
    </View>
  );

  // Render step 3: Confirm and release
  const renderStep3 = () => (
    <View style={styles.stepContainer}>
      <Text style={styles.stepTitle}>Confirm Transaction</Text>

      {transaction && selectedMerchant && (
        <>
          {/* Transaction Summary */}
          <View style={styles.transactionSummary}>
            <View style={styles.summaryHeader}>
              <Text style={styles.summaryTitle}>Transaction Details</Text>
              <View style={styles.statusBadge}>
                <Text style={styles.statusText}>Pending Release</Text>
              </View>
            </View>

            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Merchant:</Text>
              <Text style={styles.detailValue}>{selectedMerchant.name}</Text>
            </View>

            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Selling Amount:</Text>
              <Text style={styles.detailValue}>${transaction.amountUSD.toFixed(2)}</Text>
            </View>

            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>USDT to Release:</Text>
              <Text style={styles.detailValue}>{transaction.usdtAmount.toFixed(2)} USDT</Text>
            </View>

            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>You Will Receive:</Text>
              <Text style={[styles.detailValue, styles.receiveAmount]}>
                {transaction.amountLocal.toLocaleString()} {selectedCurrency}
              </Text>
            </View>

            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Exchange Rate:</Text>
              <Text style={styles.detailValue}>
                ${selectedMerchant.usdRate.toFixed(2)}/USDT
              </Text>
            </View>

            <View style={styles.divider} />

            <View style={styles.totalRow}>
              <Text style={styles.totalLabel}>Total USDT:</Text>
              <Text style={styles.totalValue}>{transaction.usdtAmount.toFixed(2)} USDT</Text>
            </View>
          </View>

          {/* Bank Details Display */}
          {bankDetails && (
            <View style={styles.bankDisplay}>
              <Text style={styles.bankDisplayTitle}>Your Bank Details</Text>
              <View style={styles.bankDisplayInfo}>
                <Text style={styles.bankDisplayText}>
                  {bankDetails.bankName} • {bankDetails.accountNumber}
                </Text>
                <Text style={styles.bankDisplayText}>
                  {bankDetails.accountName} • {selectedCurrency}
                </Text>
              </View>
            </View>
          )}

          {/* Dev Notice */}
          <View style={styles.devNotice}>
            <Icon name="developer-mode" size={16} color={GOLD_COLORS.info} />
            <Text style={styles.devNoticeText}>
              Dev Notice: The merchant will see your bank details and the local currency amount
            </Text>
          </View>

          {/* Warning Box */}
          <View style={styles.finalWarning}>
            <Icon name="warning" size={24} color={GOLD_COLORS.error} />
            <Text style={styles.finalWarningText}>
              ⚠ Never release USDT until you confirm cash in your bank App ‼
            </Text>
          </View>

          {/* Action Buttons */}
          <View style={styles.actionButtons}>
            <TouchableOpacity
              style={styles.reportButton}
              onPress={handleReport}
            >
              <Icon name="report" size={20} color={GOLD_COLORS.error} />
              <Text style={styles.reportButtonText}>Report</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.chatButton}
              onPress={handleChat}
            >
              <Icon name="chat" size={20} color={GOLD_COLORS.info} />
              <Text style={styles.chatButtonText}>Chat Box</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.releaseButton}
              onPress={handleReleaseUSDT}
            >
              <Icon name="lock-open" size={20} color="#000" />
              <Text style={styles.releaseButtonText}>Release USDT</Text>
            </TouchableOpacity>
          </View>
        </>
      )}
    </View>
  );

  // Helper function to get country flag emoji
  const getCountryFlag = (countryCode: string) => {
    const codePoints = countryCode
      .toUpperCase()
      .split('')
      .map(char => 127397 + char.charCodeAt(0));
    return String.fromCodePoint(...codePoints);
  };

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={styles.container}
      >
        <ScrollView style={styles.scrollContainer} showsVerticalScrollIndicator={false}>
          {/* Step Indicator */}
          <View style={styles.stepIndicator}>
            {[1, 2, 3].map((stepNum) => (
              <View key={stepNum} style={styles.stepContainerIndicator}>
                <View
                  style={[
                    styles.stepCircle,
                    step === stepNum && styles.activeStepCircle,
                    step > stepNum && styles.completedStepCircle,
                  ]}
                >
                  {step > stepNum ? (
                    <Icon name="check" size={16} color="#000" />
                  ) : (
                    <Text style={styles.stepNumber}>{stepNum}</Text>
                  )}
                </View>
                <Text style={styles.stepLabel}>
                  {stepNum === 1 ? 'Amount' : stepNum === 2 ? 'Merchant' : 'Release'}
                </Text>
                {stepNum < 3 && <View style={styles.stepLine} />}
              </View>
            ))}
          </View>

          {/* Main Content */}
          {step === 1 && renderStep1()}
          {step === 2 && renderStep2()}
          {step === 3 && renderStep3()}

          {/* Navigation Buttons */}
          {step < 3 && (
            <View style={styles.navigationButtons}>
              <TouchableOpacity
                style={styles.backButtonNav}
                onPress={() => step > 1 ? setStep(step - 1 as 1 | 2 | 3) : router.back()}
              >
                <Text style={styles.backButtonText}>
                  {step === 1 ? 'Cancel' : 'Back'}
                </Text>
              </TouchableOpacity>

              <TouchableOpacity
                style={[
                  styles.nextButton,
                  ((step === 1 && !usdAmount) || (step === 2 && !selectedMerchant)) &&
                  styles.nextButtonDisabled,
                ]}
                onPress={handleNextStep}
                disabled={(step === 1 && !usdAmount) || (step === 2 && !selectedMerchant)}
              >
                <Text style={styles.nextButtonText}>
                  {step === 1 ? 'Search 🔍' : 'Next'}
                </Text>
                <Icon name="arrow-forward" size={20} color="#000" />
              </TouchableOpacity>
            </View>
          )}

          {/* General Notices */}
          <View style={styles.generalNotices}>
            <View style={styles.noticeItem}>
              <Icon name="message" size={16} color={GOLD_COLORS.info} />
              <Text style={styles.noticeText}>
                Click "chat box" to upload payment proof and communicate
              </Text>
            </View>
            <View style={styles.noticeItem}>
              <Icon name="report" size={16} color={GOLD_COLORS.error} />
              <Text style={styles.noticeText}>
                Click "report" for disputes/unresolved issues
              </Text>
            </View>
            <View style={styles.noticeItem}>
              <Icon name="security" size={16} color={GOLD_COLORS.success} />
              <Text style={styles.noticeText}>
                Your USDT is locked until transaction is successfully completed
              </Text>
            </View>
          </View>
        </ScrollView>

        {/* Modals */}
        <SellTermsModal />
        <BankDetailsModal />
        <ConfirmPaymentModal />
        <OTPModal />
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

// Styles
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
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 10 : 20,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
  },
  backButton: {
    padding: 8,
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  helpButton: {
    padding: 8,
  },
  stepIndicator: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 40,
    paddingVertical: 20,
    backgroundColor: '#FFFFFF',
  },
  stepContainerIndicator: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  stepCircle: {
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: '#E5E5EA',
    justifyContent: 'center',
    alignItems: 'center',
    zIndex: 1,
  },
  activeStepCircle: {
    backgroundColor: GOLD_COLORS.primary,
  },
  completedStepCircle: {
    backgroundColor: GOLD_COLORS.success,
  },
  stepNumber: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
  },
  stepLabel: {
    fontSize: 12,
    color: '#666666',
    marginTop: 4,
    textAlign: 'center',
    position: 'absolute',
    bottom: -20,
    width: 60,
    left: -14,
  },
  stepLine: {
    flex: 1,
    height: 2,
    backgroundColor: '#E5E5EA',
    marginHorizontal: 8,
  },
  stepContainer: {
    backgroundColor: '#FFFFFF',
    marginTop: 8,
    paddingHorizontal: 20,
    paddingVertical: 24,
    borderRadius: 16,
    marginHorizontal: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  stepTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 8,
  },
  stepSubtitle: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 20,
  },
  section: {
    marginBottom: 24,
  },
  sectionTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 8,
  },
  sectionSubtitle: {
    fontSize: 13,
    color: '#666666',
    marginBottom: 16,
  },
  bankDetailsCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    borderWidth: 2,
    borderColor: GOLD_COLORS.muted,
  },
  bankHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  bankInfoHeader: {
    flex: 1,
    marginLeft: 12,
  },
  bankName: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
  },
  bankCountryBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(255, 215, 0, 0.2)',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 8,
    marginTop: 4,
    alignSelf: 'flex-start',
  },
  bankCountryEmoji: {
    fontSize: 12,
    marginRight: 4,
  },
  bankCurrency: {
    fontSize: 11,
    fontWeight: '700',
    color: GOLD_COLORS.dark,
  },
  bankInfo: {
    marginBottom: 16,
  },
  bankInfoRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  bankLabel: {
    fontSize: 14,
    color: '#666666',
  },
  bankValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  editBankButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 10,
    paddingHorizontal: 16,
    borderRadius: 12,
    alignSelf: 'flex-start',
  },
  editBankText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  setupBankButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    borderWidth: 2,
    borderColor: GOLD_COLORS.muted,
    borderStyle: 'dashed',
  },
  setupBankText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 12,
  },
  countryScroll: {
    marginHorizontal: -20,
    paddingHorizontal: 20,
  },
  countryButton: {
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 12,
    marginRight: 12,
    minWidth: 100,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedCountryButton: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  countryEmoji: {
    fontSize: 32,
    marginBottom: 8,
  },
  countryName: {
    fontSize: 12,
    fontWeight: '600',
    color: '#000000',
    textAlign: 'center',
  },
  selectedCountryName: {
    color: GOLD_COLORS.dark,
  },
  countryCurrency: {
    fontSize: 11,
    color: '#666666',
    marginTop: 4,
  },
  amountInputContainer: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    marginBottom: 16,
  },
  amountInputRow: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  currencySymbol: {
    fontSize: 32,
    fontWeight: '800',
    color: '#000000',
    marginRight: 8,
  },
  amountInput: {
    flex: 1,
    fontSize: 32,
    fontWeight: '800',
    color: '#000000',
    padding: 0,
  },
  currencyText: {
    fontSize: 20,
    fontWeight: '700',
    color: GOLD_COLORS.dark,
    marginLeft: 12,
  },
  conversionDisplay: {
    marginTop: 16,
    padding: 12,
    backgroundColor: 'rgba(255, 215, 0, 0.1)',
    borderRadius: 12,
    borderLeftWidth: 4,
    borderLeftColor: GOLD_COLORS.primary,
  },
  conversionLabel: {
    fontSize: 12,
    color: '#666666',
    marginBottom: 4,
  },
  conversionAmount: {
    fontSize: 18,
    fontWeight: '800',
    color: GOLD_COLORS.dark,
  },
  amountNote: {
    fontSize: 13,
    color: '#666666',
    marginTop: 12,
    fontWeight: '600',
  },
  quickAmounts: {
    flexDirection: 'row',
    justifyContent: 'space-between',
  },
  quickAmountButton: {
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 16,
    paddingVertical: 10,
    borderRadius: 20,
    minWidth: 70,
    alignItems: 'center',
  },
  quickAmountText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
  },
  filterSection: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 16,
  },
  filterItem: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 20,
  },
  filterText: {
    fontSize: 13,
    color: '#666666',
    marginLeft: 6,
  },
  merchantList: {
    maxHeight: 400,
  },
  merchantCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    marginBottom: 12,
    borderWidth: 2,
    borderColor: 'transparent',
    position: 'relative',
  },
  selectedMerchantCard: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  merchantHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  merchantAvatar: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  merchantAvatarText: {
    fontSize: 20,
    fontWeight: '900',
    color: '#000000',
  },
  merchantInfo: {
    flex: 1,
  },
  merchantName: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  ratingContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  ratingText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 4,
  },
  merchantTrades: {
    fontSize: 12,
    color: '#666666',
    marginTop: 2,
  },
  verificationBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F0F0F0',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 12,
  },
  verificationText: {
    fontSize: 11,
    fontWeight: '600',
    color: '#666666',
    marginLeft: 4,
  },
  merchantDetails: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 12,
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  detailItem: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  detailText: {
    fontSize: 13,
    color: '#666666',
    marginLeft: 6,
  },
  selectedOverlay: {
    position: 'absolute',
    top: 8,
    right: 8,
  },
  selectedSummary: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    marginTop: 16,
  },
  summaryRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  summaryLabel: {
    fontSize: 14,
    color: '#666666',
  },
  summaryValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  transactionSummary: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
  },
  summaryHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 16,
  },
  summaryTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
  },
  statusBadge: {
    backgroundColor: GOLD_COLORS.warning,
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 12,
  },
  statusText: {
    fontSize: 12,
    fontWeight: '700',
    color: '#000000',
  },
  //@ts-expect-error
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 12,
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
  receiveAmount: {
    fontSize: 16,
    fontWeight: '800',
    color: GOLD_COLORS.success,
  },
  divider: {
    height: 1,
    backgroundColor: '#E5E5EA',
    marginVertical: 16,
  },
  totalRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  totalLabel: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  totalValue: {
    fontSize: 24,
    fontWeight: '900',
    color: GOLD_COLORS.primary,
  },
  bankDisplay: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
    borderLeftWidth: 4,
    borderLeftColor: GOLD_COLORS.primary,
  },
  bankDisplayTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 8,
  },
  bankDisplayInfo: {
    gap: 4,
  },
  bankDisplayText: {
    fontSize: 13,
    color: '#666666',
  },
  devNotice: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    padding: 12,
    borderRadius: 12,
    marginBottom: 16,
  },
  devNoticeText: {
    fontSize: 12,
    color: GOLD_COLORS.info,
    marginLeft: 8,
    flex: 1,
    fontStyle: 'italic',
  },
  finalWarning: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFE5E5',
    padding: 16,
    borderRadius: 12,
    marginBottom: 20,
  },
  finalWarningText: {
    fontSize: 14,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginLeft: 12,
    flex: 1,
  },
  actionButtons: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    gap: 12,
  },
  reportButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#FFE5E5',
    paddingVertical: 16,
    borderRadius: 16,
  },
  reportButtonText: {
    fontSize: 14,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginLeft: 8,
  },
  chatButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#E5F7FF',
    paddingVertical: 16,
    borderRadius: 16,
  },
  chatButtonText: {
    fontSize: 14,
    fontWeight: '700',
    color: GOLD_COLORS.info,
    marginLeft: 8,
  },
  releaseButton: {
    flex: 2,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 16,
    borderRadius: 16,
  },
  releaseButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 8,
  },
  navigationButtons: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingBottom: 30,
  },
  backButtonNav: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
    marginRight: 8,
  },
  backButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  nextButton: {
    flex: 2,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 16,
    borderRadius: 16,
    marginLeft: 8,
  },
  nextButtonDisabled: {
    opacity: 0.5,
  },
  nextButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
    marginRight: 8,
  },
  generalNotices: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 20,
    marginBottom: 30,
    padding: 16,
    borderRadius: 16,
  },
  noticeItem: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  noticeText: {
    fontSize: 12,
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
    maxWidth: 400,
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
  },
  modalCloseButton: {
    padding: 4,
  },
  modalBody: {
    padding: 24,
  },
  modalFooter: {
    flexDirection: 'row',
    padding: 24,
    paddingTop: 0,
    gap: 12,
  },
  warningBox: {
    alignItems: 'center',
    marginBottom: 20,
  },
  warningTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: GOLD_COLORS.warning,
    marginTop: 12,
  },
  termsText: {
    fontSize: 14,
    lineHeight: 20,
    color: '#666666',
    marginBottom: 20,
  },
  merchantNotice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#E5F7FF',
    padding: 16,
    borderRadius: 12,
    marginBottom: 20,
  },
  merchantNoticeText: {
    fontSize: 13,
    color: GOLD_COLORS.info,
    marginLeft: 8,
    flex: 1,
    fontStyle: 'italic',
  },
  securityTips: {
    backgroundColor: GOLD_COLORS.light,
    padding: 16,
    borderRadius: 12,
  },
  securityTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  tipItem: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  tipText: {
    fontSize: 13,
    color: '#666666',
    marginLeft: 8,
    flex: 1,
  },
  cancelButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  cancelButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  proceedButton: {
    flex: 2,
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  proceedButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },
  // Bank Form Styles
  bankFormContainer: {
    gap: 16,
  },
  formLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 4,
  },
  formInput: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    fontSize: 16,
    color: '#000000',
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  countryDisplay: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  countryDisplayEmoji: {
    fontSize: 24,
    marginRight: 12,
  },
  countryDisplayText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    flex: 1,
  },
  countryCurrencyDisplay: {
    fontSize: 16,
    color: GOLD_COLORS.dark,
    fontWeight: '700',
  },
  formNote: {
    fontSize: 12,
    color: '#666666',
    fontStyle: 'italic',
    marginTop: 8,
  },
  saveButton: {
    flex: 2,
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  saveButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },
  // Confirm Modal Styles
  confirmIcon: {
    alignItems: 'center',
    marginBottom: 20,
  },
  confirmTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    textAlign: 'center',
    marginBottom: 12,
  },
  confirmText: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    marginBottom: 20,
    lineHeight: 20,
  },
  warningAlert: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFE5E5',
    padding: 16,
    borderRadius: 12,
  },
  warningAlertText: {
    fontSize: 13,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginLeft: 8,
    flex: 1,
  },
  noButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  noButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  yesButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.success,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  yesButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },
  // OTP Modal Styles
  otpIcon: {
    alignItems: 'center',
    marginBottom: 20,
  },
  otpTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    textAlign: 'center',
    marginBottom: 8,
  },
  otpText: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    marginBottom: 24,
    lineHeight: 20,
  },
  otpInput: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
    textAlign: 'center',
    letterSpacing: 8,
    marginBottom: 16,
  },
  resendButton: {
    alignItems: 'center',
  },
  resendText: {
    fontSize: 14,
    color: GOLD_COLORS.info,
    fontWeight: '600',
  },
  submitButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    paddingVertical: 16,
    borderRadius: 16,
    alignItems: 'center',
  },
  submitButtonDisabled: {
    opacity: 0.5,
  },
  submitButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },
});

export default WithdrawalScreen;