// screens/sendFiat.tsx
//
// RETIRED (2026-07-27): nothing navigates here any more. Its merchant catalog
// was hardcoded, so the flow could never actually pay out. Fiat withdrawal now
// runs through Instant Transfer (app/wallet/it/sell.tsx) and the P2P /
// Payment Merchant markets, reachable from the wallet's Withdraw sheet.
// Kept for reference until the replacement rails have run in production.
//
// Sell-USDT-for-fiat withdrawal flow — a 3-step wizard (amount → merchant →
// release) with bank-details setup, country selection, a sell-terms notice, a
// payment-confirmation gate, and biometric/PIN authorization before release.
// Migrated onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). All state, the step machine, the merchant catalog, every
// amount/rate/conversion computation, navigation, and the AuthorizationModal
// gating are unchanged — this is a visual/structural restyle only.
import { router } from 'expo-router';
import React, { useState, useEffect } from 'react';
import {
  View,
  ScrollView,
  TextInput,
  Platform,
  KeyboardAvoidingView,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Card,
  Divider,
  Icon,
  Input,
  PressableScale,
  Sheet,
  Text,
} from '@/components/ui';
import AuthorizationModal from '../components/common/AuthorizationModal';
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
  const { colors, spacing } = useTheme();
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center' }}>
      <Icon name="star" size={size} color={colors.warning} />
      <Text variant="bodySm" weight="700" style={{ fontSize: size - 2, marginLeft: spacing.xs }}>
        {rating.toFixed(1)}
      </Text>
    </View>
  );
};

// Merchant Card Component
const MerchantCard: React.FC<{
  merchant: Merchant;
  isSelected: boolean;
  onSelect: (merchant: Merchant) => void;
}> = ({ merchant, isSelected, onSelect }) => {
  const { colors, spacing, radius } = useTheme();
  const isHighVerification = merchant.verificationLevel === 'high';
  return (
    <PressableScale onPress={() => onSelect(merchant)} style={{ marginBottom: spacing.md }}>
      <Card
        padding="lg"
        style={{
          borderWidth: 1.5,
          borderColor: isSelected ? colors.accent : 'transparent',
          backgroundColor: isSelected ? colors.accentMuted : colors.surface,
        }}
      >
        <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.md }}>
          <View
            style={{
              width: 48,
              height: 48,
              borderRadius: radius.pill,
              backgroundColor: colors.accent,
              justifyContent: 'center',
              alignItems: 'center',
              marginRight: spacing.md,
            }}
          >
            <Text variant="title2" weight="800" style={{ color: colors.onAccent }}>
              {merchant.name.charAt(0).toUpperCase()}
            </Text>
          </View>
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
              {merchant.name}
            </Text>
            <MerchantRating rating={merchant.rating} />
            <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
              {merchant.completedTrades.toLocaleString()} trades
            </Text>
          </View>
          <Badge
            label={isHighVerification ? 'Verified' : 'Basic'}
            icon="verified"
            color={isHighVerification ? colors.success : colors.warning}
          />
        </View>

        <View
          style={{
            backgroundColor: colors.surfaceAlt,
            borderRadius: radius.md,
            padding: spacing.md,
          }}
        >
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.sm }}>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="pricetag" size={14} color={colors.textSecondary} />
              <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
                Rate: ${merchant.usdRate.toFixed(2)}/USDT
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="clock" size={14} color={colors.textSecondary} />
              <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
                {merchant.responseTime}
              </Text>
            </View>
          </View>

          <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="swapVertical" size={14} color={colors.textSecondary} />
              <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
                Limit: ${merchant.minAmount}-${merchant.maxAmount}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="bank" size={14} color={colors.textSecondary} />
              <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
                Bank Transfer
              </Text>
            </View>
          </View>
        </View>

        {isSelected && (
          <View style={{ position: 'absolute', top: spacing.sm, right: spacing.sm }}>
            <Icon name="checkCircle" size={24} color={colors.success} />
          </View>
        )}
      </Card>
    </PressableScale>
  );
};

// Main WithdrawalScreen Component
const WithdrawalScreen: React.FC = () => {
  const { colors, spacing, radius, type, fonts } = useTheme();

  // State for withdrawal steps
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [isLoading, setIsLoading] = useState(false);
  const [showSellModal, setShowSellModal] = useState(false);
  const [showConfirmModal, setShowConfirmModal] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);
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
      setShowAuthModal(true);
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

  // Handle authorization success — runs after biometric/PIN/OTP auth is verified
  const handleAuthorized = async () => {
    setShowAuthModal(false);
    setIsLoading(true);

    // Simulate API call
    setTimeout(() => {
      setIsLoading(false);

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

  // Sell Terms Sheet
  const SellTermsModal = () => (
    <Sheet visible={showSellModal} onClose={() => setShowSellModal(false)} title="Sell USDT">
      <ScrollView
        style={{ maxHeight: 420 }}
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.lg }}
      >
        <View style={{ alignItems: 'center', marginBottom: spacing.lg }}>
          <Icon name="warning" size={24} color={colors.warning} />
          <Text variant="title3" weight="700" style={{ color: colors.warning, marginTop: spacing.md }}>
            Important Notice
          </Text>
        </View>

        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Dear user, our registered merchant will pay fiat to your bank account
          (following your provided bank account and selected country), and the merchant
          will upload their payment proof 🧾, wait for your verification of their payment
          from your Mobile bank account of which you must be sure that you have seen the
          equivalent amount payment from the merchant on your mobile bank account before
          releasing the USDT to our registered merchant account.
        </Text>

        <AccentCard railColor={colors.info} padding="md" style={{ backgroundColor: colors.infoBg, marginBottom: spacing.lg }}>
          <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm }}>
            <Icon name="info" size={20} color={colors.info} />
            <Text variant="bodySm" style={{ color: colors.info, flex: 1, fontStyle: 'italic' }}>
              PS: Dear merchant, user USDT is locked with us. Don&apos;t send fake payment or you will be sanctioned!
            </Text>
          </View>
        </AccentCard>

        <Card padding="lg" style={{ backgroundColor: colors.surfaceAlt }}>
          <Text variant="bodySm" weight="700" style={{ marginBottom: spacing.md }}>
            Security Tips:
          </Text>
          {[
            'Always verify payment in your bank app',
            'Never release USDT before confirming receipt',
            'Use chat box for communication',
          ].map((tip) => (
            <View key={tip} style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.sm }}>
              <Icon name="checkCircle" size={16} color={colors.success} />
              <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.sm, flex: 1 }}>
                {tip}
              </Text>
            </View>
          ))}
        </Card>
      </ScrollView>

      <View style={{ flexDirection: 'row', gap: spacing.md, paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
        <Button title="Cancel" variant="secondary" onPress={() => setShowSellModal(false)} style={{ flex: 1 }} />
        <Button title="Understood & Proceed" onPress={handleProceedSell} style={{ flex: 2 }} />
      </View>
    </Sheet>
  );

  // Bank Details Sheet
  const BankDetailsModal = () => (
    <Sheet visible={showBankModal} onClose={() => setShowBankModal(false)} title="Setup Bank Details">
      <ScrollView
        style={{ maxHeight: 460 }}
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.lg, gap: spacing.lg }}
        keyboardShouldPersistTaps="handled"
      >
        <Input
          label="Bank Name"
          value={bankForm.bankName}
          onChangeText={(text) => setBankForm({ ...bankForm, bankName: text })}
          placeholder="e.g., Chase Bank"
        />

        <Input
          label="Account Number"
          value={bankForm.accountNumber}
          onChangeText={(text) => setBankForm({ ...bankForm, accountNumber: text })}
          placeholder="Enter account number"
          keyboardType="number-pad"
        />

        <Input
          label="Account Holder Name"
          value={bankForm.accountName}
          onChangeText={(text) => setBankForm({ ...bankForm, accountName: text })}
          placeholder="Enter full name as on bank account"
        />

        <View>
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
            Country
          </Text>
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              backgroundColor: colors.surface,
              borderRadius: radius.md,
              padding: spacing.lg,
              borderWidth: 1.5,
              borderColor: colors.border,
            }}
          >
            <Text style={{ fontSize: 24, marginRight: spacing.md }}>
              {getCountryFlag(selectedCountry)}
            </Text>
            <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
              {COUNTRIES.find(c => c.code === selectedCountry)?.name}
            </Text>
            <Text variant="bodyMed" weight="700" color="accent">
              ({selectedCurrency})
            </Text>
          </View>
          <Text variant="caption" color="textMuted" style={{ fontStyle: 'italic', marginTop: spacing.sm }}>
            Note: Currency will be automatically set based on selected country
          </Text>
        </View>
      </ScrollView>

      <View style={{ flexDirection: 'row', gap: spacing.md, paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
        <Button title="Cancel" variant="secondary" onPress={() => setShowBankModal(false)} style={{ flex: 1 }} />
        <Button title="Save Bank Details" onPress={handleSaveBankDetails} style={{ flex: 2 }} />
      </View>
    </Sheet>
  );

  // Confirm Payment Sheet
  const ConfirmPaymentModal = () => (
    <Sheet visible={showConfirmModal} onClose={() => setShowConfirmModal(false)} title="Confirm Payment">
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md }}>
        <View style={{ alignItems: 'center', marginBottom: spacing.lg }}>
          <Icon name="bank" size={60} color={colors.accent} />
        </View>

        <Text variant="title3" weight="700" align="center" style={{ marginBottom: spacing.md }}>
          Confirm you have received cash in your bank
        </Text>

        <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing.lg }}>
          Have you verified in your mobile banking app that the payment from the merchant has been received?
        </Text>

        <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg, marginBottom: spacing.lg }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Icon name="warning" size={20} color={colors.danger} />
            <Text variant="bodySm" weight="700" style={{ color: colors.danger, flex: 1 }}>
              ⚠ Never release USDT until you confirm cash in your bank App ‼
            </Text>
          </View>
        </AccentCard>
      </View>

      <View style={{ flexDirection: 'row', gap: spacing.md, paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
        <Button title="No" variant="secondary" onPress={() => handleConfirmPayment(false)} style={{ flex: 1 }} />
        <Button title="Yes" variant="success" onPress={() => handleConfirmPayment(true)} style={{ flex: 1 }} />
      </View>
    </Sheet>
  );

  // Authorization Modal (replaces OTP Modal)
  const renderAuthModal = () => (
    <AuthorizationModal
      visible={showAuthModal}
      title="Authorize Payment"
      description={`Send $${usdAmount || '0'} to ${selectedMerchant?.name ?? 'merchant'}`}
      onAuthorized={handleAuthorized}
      onCancel={() => setShowAuthModal(false)}
    />
  );

  // Render step 1: Setup and amount
  const renderStep1 = () => (
    <View>
      <Text variant="title2" weight="700" style={{ marginBottom: spacing.sm }}>
        Sell USDT for Fiat
      </Text>

      {/* Bank Details Section */}
      <View style={{ marginBottom: spacing['2xl'] }}>
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
          Bank Details
        </Text>

        {hasBankDetails && bankDetails ? (
          <AccentCard padding="lg">
            <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.lg }}>
              <Icon name="bank" size={24} color={colors.accent} />
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="title3" weight="700">
                  {bankDetails.bankName}
                </Text>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.xs, alignSelf: 'flex-start' }}>
                  <Text style={{ fontSize: 12 }}>
                    {getCountryFlag(bankDetails.country)}
                  </Text>
                  <Badge label={bankDetails.currency} color={colors.accent} background={colors.accentMuted} size="sm" />
                </View>
              </View>
            </View>

            <View style={{ marginBottom: spacing.lg }}>
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.sm }}>
                <Text variant="bodySm" color="textSecondary">Account Number:</Text>
                <Text variant="bodySm" weight="600">{bankDetails.accountNumber}</Text>
              </View>
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.sm }}>
                <Text variant="bodySm" color="textSecondary">Account Name:</Text>
                <Text variant="bodySm" weight="600">{bankDetails.accountName}</Text>
              </View>
              <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                <Text variant="bodySm" color="textSecondary">Country:</Text>
                <Text variant="bodySm" weight="600">
                  {COUNTRIES.find(c => c.code === bankDetails.country)?.name}
                </Text>
              </View>
            </View>

            <Button
              title="Edit Bank Details"
              leftIcon="edit"
              size="sm"
              fullWidth={false}
              onPress={handleEditBank}
            />
          </AccentCard>
        ) : (
          <PressableScale onPress={handleSetupBank}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                justifyContent: 'center',
                backgroundColor: colors.accentMuted,
                borderRadius: radius.lg,
                padding: spacing.xl,
                borderWidth: 1.5,
                borderColor: colors.accent,
                borderStyle: 'dashed',
              }}
            >
              <Icon name="plus" size={24} color={colors.accent} />
              <Text variant="bodyMed" weight="700" color="accent" style={{ marginLeft: spacing.md }}>
                Setup Bank Details
              </Text>
            </View>
          </PressableScale>
        )}
      </View>

      {/* Country Selection */}
      <View style={{ marginBottom: spacing['2xl'] }}>
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
          Select Country
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Choose country that supports your bank
        </Text>

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={{ marginHorizontal: -spacing.lg }}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, gap: spacing.md }}
        >
          {COUNTRIES.map((country) => {
            const isSel = selectedCountry === country.code;
            return (
              <PressableScale
                key={country.code}
                onPress={() => setSelectedCountry(country.code)}
                style={{
                  alignItems: 'center',
                  backgroundColor: isSel ? colors.accentMuted : colors.surfaceAlt,
                  borderRadius: radius.md,
                  padding: spacing.md,
                  minWidth: 100,
                  borderWidth: 1.5,
                  borderColor: isSel ? colors.accent : 'transparent',
                }}
              >
                <Text style={{ fontSize: 32, marginBottom: spacing.sm }}>
                  {getCountryFlag(country.code)}
                </Text>
                <Text
                  variant="caption"
                  weight="600"
                  align="center"
                  color={isSel ? 'accent' : 'textPrimary'}
                >
                  {country.name}
                </Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                  {country.currency} {country.symbol}
                </Text>
              </PressableScale>
            );
          })}
        </ScrollView>
      </View>

      {/* Amount Input */}
      <View style={{ marginBottom: spacing['2xl'] }}>
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
          Enter Amount to Sell
        </Text>

        <Card padding="xl" style={{ backgroundColor: colors.surfaceAlt, marginBottom: spacing.lg }}>
          <View style={{ flexDirection: 'row', alignItems: 'center' }}>
            <Text variant="display" weight="800" style={{ marginRight: spacing.sm }}>
              $
            </Text>
            <TextInput
              style={{
                flex: 1,
                fontSize: type.display.fontSize,
                fontFamily: fonts['800'],
                color: colors.textPrimary,
                padding: 0,
              }}
              value={usdAmount}
              onChangeText={setUsdAmount}
              placeholder="0.00"
              placeholderTextColor={colors.textMuted}
              keyboardType="decimal-pad"
              returnKeyType="done"
            />
            <Text variant="title2" weight="700" color="accent" style={{ marginLeft: spacing.md }}>
              USD
            </Text>
          </View>

          <View
            style={{
              marginTop: spacing.lg,
              padding: spacing.md,
              backgroundColor: colors.accentMuted,
              borderRadius: radius.md,
              borderLeftWidth: 4,
              borderLeftColor: colors.accent,
            }}
          >
            <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.xs }}>
              You will receive:
            </Text>
            <Text variant="title3" weight="800" color="accent">
              {selectedCurrency} {selectedMerchant ? calculateLocalAmount().toLocaleString() : '0.00'}
            </Text>
          </View>

          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.md }}>
            Available balance: 5,000 USDT (≈ $5,000)
          </Text>
        </Card>

        {/* Quick Amount Buttons */}
        <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
          {[100, 500, 1000, 5000].map((amount) => (
            <PressableScale
              key={amount}
              onPress={() => setUsdAmount(amount.toString())}
              style={{
                backgroundColor: colors.surfaceAlt,
                paddingHorizontal: spacing.lg,
                paddingVertical: spacing.sm,
                borderRadius: radius.pill,
                minWidth: 70,
                alignItems: 'center',
              }}
            >
              <Text variant="bodySm" weight="700">
                ${amount}
              </Text>
            </PressableScale>
          ))}
        </View>
      </View>
    </View>
  );

  // Render step 2: Select merchant
  const renderStep2 = () => (
    <View>
      <Text variant="title2" weight="700" style={{ marginBottom: spacing.sm }}>
        Select Merchant
      </Text>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
        Available merchants for ${usdAmount}
      </Text>

      <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.lg }}>
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            backgroundColor: colors.surfaceAlt,
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.sm,
            borderRadius: radius.pill,
          }}
        >
          <Icon name="filter" size={18} color={colors.textSecondary} />
          <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
            Sort by: Best Rate
          </Text>
        </View>
        <PressableScale
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            backgroundColor: colors.surfaceAlt,
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.sm,
            borderRadius: radius.pill,
          }}
        >
          <Icon name="filter" size={18} color={colors.textSecondary} />
          <Text variant="bodySm" color="textSecondary" style={{ marginLeft: spacing.xs }}>
            Filters
          </Text>
        </PressableScale>
      </View>

      <ScrollView style={{ maxHeight: 400 }}>
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
        <Card padding="lg" style={{ backgroundColor: colors.surfaceAlt, marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.sm }}>
            <Text variant="bodySm" color="textSecondary">Selected Rate:</Text>
            <Text variant="bodySm" weight="600">
              ${selectedMerchant.usdRate.toFixed(2)}/USDT
            </Text>
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.sm }}>
            <Text variant="bodySm" color="textSecondary">Payment Method:</Text>
            <Text variant="bodySm" weight="600">Bank Transfer</Text>
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
            <Text variant="bodySm" color="textSecondary">You Will Receive:</Text>
            <Text variant="bodySm" weight="600">
              {calculateLocalAmount().toLocaleString()} {selectedCurrency}
            </Text>
          </View>
        </Card>
      )}
    </View>
  );

  // Render step 3: Confirm and release
  const renderStep3 = () => (
    <View>
      <Text variant="title2" weight="700" style={{ marginBottom: spacing.sm }}>
        Confirm Transaction
      </Text>

      {transaction && selectedMerchant && (
        <>
          {/* Transaction Summary */}
          <Card padding="xl" style={{ backgroundColor: colors.surfaceAlt, marginBottom: spacing.xl }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.lg }}>
              <Text variant="title3" weight="700">Transaction Details</Text>
              <Badge label="Pending Release" color={colors.warning} background={colors.warningBg} />
            </View>

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">Merchant:</Text>
              <Text variant="bodySm" weight="600">{selectedMerchant.name}</Text>
            </View>

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">Selling Amount:</Text>
              <Text variant="bodySm" weight="600">${transaction.amountUSD.toFixed(2)}</Text>
            </View>

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">USDT to Release:</Text>
              <Text variant="bodySm" weight="600">{transaction.usdtAmount.toFixed(2)} USDT</Text>
            </View>

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">You Will Receive:</Text>
              <Text variant="bodyMed" weight="800" style={{ color: colors.success }}>
                {transaction.amountLocal.toLocaleString()} {selectedCurrency}
              </Text>
            </View>

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginBottom: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">Exchange Rate:</Text>
              <Text variant="bodySm" weight="600">
                ${selectedMerchant.usdRate.toFixed(2)}/USDT
              </Text>
            </View>

            <Divider spacing="lg" />

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
              <Text variant="title3" weight="700">Total USDT:</Text>
              <Text variant="title1" weight="800" color="accent">{transaction.usdtAmount.toFixed(2)} USDT</Text>
            </View>
          </Card>

          {/* Bank Details Display */}
          {bankDetails && (
            <View
              style={{
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.md,
                padding: spacing.lg,
                marginBottom: spacing.lg,
                borderLeftWidth: 4,
                borderLeftColor: colors.accent,
              }}
            >
              <Text variant="bodySm" weight="700" style={{ marginBottom: spacing.sm }}>
                Your Bank Details
              </Text>
              <View style={{ gap: spacing.xs }}>
                <Text variant="bodySm" color="textSecondary">
                  {bankDetails.bankName} • {bankDetails.accountNumber}
                </Text>
                <Text variant="bodySm" color="textSecondary">
                  {bankDetails.accountName} • {selectedCurrency}
                </Text>
              </View>
            </View>
          )}

          {/* Dev Notice */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              backgroundColor: colors.surfaceAlt,
              padding: spacing.md,
              borderRadius: radius.md,
              marginBottom: spacing.lg,
            }}
          >
            <Icon name="info" size={16} color={colors.info} />
            <Text variant="caption" style={{ color: colors.info, marginLeft: spacing.sm, flex: 1, fontStyle: 'italic' }}>
              Dev Notice: The merchant will see your bank details and the local currency amount
            </Text>
          </View>

          {/* Warning Box */}
          <AccentCard railColor={colors.danger} padding="lg" style={{ backgroundColor: colors.dangerBg, marginBottom: spacing.xl }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <Icon name="warning" size={24} color={colors.danger} />
              <Text variant="bodySm" weight="700" style={{ color: colors.danger, flex: 1 }}>
                ⚠ Never release USDT until you confirm cash in your bank App ‼
              </Text>
            </View>
          </AccentCard>

          {/* Action Buttons */}
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', gap: spacing.md }}>
            <Button
              title="Report"
              variant="secondary"
              leftIcon="dispute"
              onPress={handleReport}
              style={{ flex: 1, backgroundColor: colors.dangerBg, borderColor: colors.danger }}
            />
            <Button
              title="Chat Box"
              variant="secondary"
              leftIcon="mail"
              onPress={handleChat}
              style={{ flex: 1, backgroundColor: colors.infoBg, borderColor: colors.info }}
            />
            <Button
              title="Release USDT"
              leftIcon="lock"
              onPress={handleReleaseUSDT}
              style={{ flex: 2 }}
            />
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
        >
          {/* Step Indicator */}
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingHorizontal: spacing.xl, marginBottom: spacing['3xl'] }}>
            {[1, 2, 3].map((stepNum) => (
              <View key={stepNum} style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
                <View
                  style={{
                    width: 32,
                    height: 32,
                    borderRadius: radius.pill,
                    backgroundColor:
                      step === stepNum ? colors.accent : step > stepNum ? colors.success : colors.surfaceAlt,
                    justifyContent: 'center',
                    alignItems: 'center',
                    zIndex: 1,
                  }}
                >
                  {step > stepNum ? (
                    <Icon name="check" size={16} color={colors.onAccent} />
                  ) : (
                    <Text variant="bodySm" weight="700" style={{ color: step === stepNum ? colors.onAccent : colors.textSecondary }}>
                      {stepNum}
                    </Text>
                  )}
                </View>
                <Text
                  variant="caption"
                  color="textSecondary"
                  align="center"
                  style={{ marginTop: spacing.xs, position: 'absolute', bottom: -20, width: 60, left: -14 }}
                >
                  {stepNum === 1 ? 'Amount' : stepNum === 2 ? 'Merchant' : 'Release'}
                </Text>
                {stepNum < 3 && (
                  <View style={{ flex: 1, height: 2, backgroundColor: colors.border, marginHorizontal: spacing.sm }} />
                )}
              </View>
            ))}
          </View>

          {/* Main Content */}
          {step === 1 && renderStep1()}
          {step === 2 && renderStep2()}
          {step === 3 && renderStep3()}

          {/* Navigation Buttons */}
          {step < 3 && (
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', gap: spacing.md, marginTop: spacing.sm }}>
              <Button
                title={step === 1 ? 'Cancel' : 'Back'}
                variant="secondary"
                onPress={() => step > 1 ? setStep(step - 1 as 1 | 2 | 3) : router.back()}
                style={{ flex: 1 }}
              />
              <Button
                title={step === 1 ? 'Search 🔍' : 'Next'}
                rightIcon="arrowRight"
                onPress={handleNextStep}
                disabled={(step === 1 && !usdAmount) || (step === 2 && !selectedMerchant)}
                style={{ flex: 2 }}
              />
            </View>
          )}

          {/* General Notices */}
          <Card padding="lg" style={{ marginTop: spacing['2xl'] }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.md }}>
              <Icon name="mail" size={16} color={colors.info} />
              <Text variant="caption" color="textSecondary" style={{ marginLeft: spacing.sm, flex: 1 }}>
                Click &quot;chat box&quot; to upload payment proof and communicate
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.md }}>
              <Icon name="dispute" size={16} color={colors.danger} />
              <Text variant="caption" color="textSecondary" style={{ marginLeft: spacing.sm, flex: 1 }}>
                Click &quot;report&quot; for disputes/unresolved issues
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="shield" size={16} color={colors.success} />
              <Text variant="caption" color="textSecondary" style={{ marginLeft: spacing.sm, flex: 1 }}>
                Your USDT is locked until transaction is successfully completed
              </Text>
            </View>
          </Card>
        </ScrollView>

        {/* Modals */}
        <SellTermsModal />
        <BankDetailsModal />
        <ConfirmPaymentModal />
        {renderAuthModal()}
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default WithdrawalScreen;
