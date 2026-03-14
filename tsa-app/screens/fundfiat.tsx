// DepositScreen.tsx - MERCHANT-BASED EXCHANGE RATES (WITH FIXED CHAT MODAL)
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  Animated,
  StyleSheet,
  Platform,
  Alert,
  Modal,
  TextInput,
  KeyboardAvoidingView,
  ImageBackground,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
// Types and interfaces
interface Merchant {
  id: string;
  name: string;
  rating: number;
  completedTrades: number;
  minAmount: number;
  maxAmount: number;
  responseTime: string;
  paymentMethods: string[];
  verificationLevel: string;
  exchangeRate: number; // Merchant-specific exchange rate
  bankDetails?: BankDetails;
}

interface BankDetails {
  bankName: string;
  accountNumber: string;
  accountName: string;
  country: string;
  currency: string;
}

interface Transaction {
  id: string;
  merchantId: string;
  amountUSD: number;
  amountLocal: number;
  usdtAmount: number;
  status: 'pending' | 'processing' | 'completed' | 'disputed';
  timestamp: Date;
  merchantExchangeRate: number; // Store the merchant's exchange rate used
}

interface CountryData {
  code: string;
  name: string;
  currency: string;
  symbol: string;
}

interface ChatMessage {
  id: string;
  sender: 'user' | 'merchant' | 'system';
  message: string;
  timestamp: Date;
  type: 'text' | 'image' | 'system';
  imageUri?: string;
}

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#34C759',
  warning: '#FF9500',
  error: '#FF3B30',
  info: '#5AC8FA',
};

// Country data (without system rates)
const COUNTRIES_DATA: CountryData[] = [
  {
    code: 'US',
    name: 'United States',
    currency: 'USD',
    symbol: '$'
  },
  {
    code: 'NG',
    name: 'Nigeria',
    currency: 'NGN',
    symbol: '₦'
  },
  {
    code: 'UK',
    name: 'United Kingdom',
    currency: 'GBP',
    symbol: '£'
  },
];

// Helper function to get country flag emoji
const getCountryFlag = (countryCode: string): string => {
  const code = countryCode.toUpperCase();
  if (code.length !== 2) return '🏳️';

  const firstChar = String.fromCodePoint(0x1F1E6 - 65 + code.charCodeAt(0));
  const secondChar = String.fromCodePoint(0x1F1E6 - 65 + code.charCodeAt(1));

  return firstChar + secondChar;
};

// Merchant Rating Component
const MerchantRating: React.FC<{ rating: number; size?: number }> = ({ rating, size = 16 }) => {
  return (
    <View style={styles.ratingContainer}>
      <Icon name="star" size={size} color="#FFD700" />
      <Text style={[styles.ratingText, { fontSize: size - 2 }]}>{rating.toFixed(1)}</Text>
    </View>
  );
};

// Bank Details Component
const BankDetailsDisplay: React.FC<{
  bankDetails: BankDetails;
  amountLocal: number;
  exchangeRate: number;
}> = ({ bankDetails, amountLocal, exchangeRate }) => {
  const handleCopyAccountNumber = () => {
    Alert.alert('Copied!', 'Account number copied to clipboard');
  };

  const handleCopyAccountName = () => {
    Alert.alert('Copied!', 'Account name copied to clipboard');
  };

  return (
    <View style={styles.bankDetailsContainer}>
      <View style={styles.bankDetailsHeader}>
        <Icon name="account-balance" size={24} color={GOLD_COLORS.primary} />
        <Text style={styles.bankDetailsTitle}>Merchant Bank Details</Text>
      </View>

      <View style={styles.warningAlert}>
        <Icon name="warning" size={20} color={GOLD_COLORS.warning} />
        <Text style={styles.warningAlertText}>
          ⚠ Deposit "{amountLocal.toLocaleString()} {bankDetails.currency}" to the above bank details only. Do not entertain other bank details!
        </Text>
      </View>

      <View style={styles.bankInfoCard}>
        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Bank Name:</Text>
          <Text style={styles.bankInfoValue}>{bankDetails.bankName}</Text>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Account Number:</Text>
          <View style={styles.copyableRow}>
            <Text style={styles.bankInfoValue}>{bankDetails.accountNumber}</Text>
            <TouchableOpacity style={styles.copyButtonSmall} onPress={handleCopyAccountNumber}>
              <Icon name="content-copy" size={16} color={GOLD_COLORS.primary} />
            </TouchableOpacity>
          </View>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Account Name:</Text>
          <View style={styles.copyableRow}>
            <Text style={styles.bankInfoValue}>{bankDetails.accountName}</Text>
            <TouchableOpacity style={styles.copyButtonSmall} onPress={handleCopyAccountName}>
              <Icon name="content-copy" size={16} color={GOLD_COLORS.primary} />
            </TouchableOpacity>
          </View>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Country:</Text>
          <View style={styles.countryBadge}>
            <Text style={styles.flagEmoji}>
              {getCountryFlag(bankDetails.country)}
            </Text>
            <Text style={styles.countryText}>{bankDetails.country}</Text>
          </View>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Currency:</Text>
          <Text style={[styles.bankInfoValue, styles.currencyHighlight]}>
            {bankDetails.currency}
          </Text>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Merchant Exchange Rate:</Text>
          <Text style={[styles.bankInfoValue, styles.currencyHighlight]}>
            1 USD = {exchangeRate.toLocaleString()} {bankDetails.currency}
          </Text>
        </View>

        <View style={styles.bankInfoRow}>
          <Text style={styles.bankInfoLabel}>Amount to Deposit:</Text>
          <Text style={[styles.bankInfoValue, styles.amountHighlight]}>
            {amountLocal.toLocaleString()} {bankDetails.currency}
          </Text>
        </View>
      </View>

      <View style={styles.securityNote}>
        <Icon name="security" size={14} color={GOLD_COLORS.success} />
        <Text style={styles.securityNoteText}>
          Merchant USDT is locked with TSA Connect. Send valid payments only.
        </Text>
      </View>
    </View>
  );
};

// Merchant Card Component - Updated to show merchant's exchange rate
const MerchantCard: React.FC<{
  merchant: Merchant;
  isSelected: boolean;
  onSelect: (merchant: Merchant) => void;
  showDetails?: boolean;
  selectedCurrency: string;
}> = ({ merchant, isSelected, onSelect, showDetails = false, selectedCurrency }) => {
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
            <Icon name="currency-exchange" size={14} color="#666" />
            <Text style={styles.detailText}>
              Rate: {merchant.exchangeRate.toLocaleString()} {selectedCurrency}/USDT
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

      {showDetails && isSelected && merchant.bankDetails && (
        <View style={styles.merchantBankPreview}>
          <Text style={styles.bankPreviewTitle}>Bank: {merchant.bankDetails.bankName}</Text>
          <Text style={styles.bankPreviewAccount}>
            Account: ••••{merchant.bankDetails.accountNumber.slice(-4)}
          </Text>
        </View>
      )}
    </TouchableOpacity>
  );
};

// Terms Modal Component
const TermsModal: React.FC<{
  visible: boolean;
  onClose: () => void;
  onProceed: () => void;
}> = ({ visible, onClose, onProceed }) => {
  const modalAnimation = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    if (visible) {
      Animated.spring(modalAnimation, {
        toValue: 1,
        tension: 50,
        friction: 7,
        useNativeDriver: true,
      }).start();
    } else {
      modalAnimation.setValue(0);
    }
  }, [visible]);

  const translateY = modalAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [300, 0],
  });

  const opacity = modalAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [0, 1],
  });

  return (
    <Modal
      visible={visible}
      transparent
      animationType="none"
      onRequestClose={onClose}
    >
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={styles.modalOverlay}
      >
        <Animated.View style={[styles.modalOverlay, { opacity }]}>
          <TouchableOpacity
            style={styles.modalBackdrop}
            activeOpacity={1}
            onPress={onClose}
          />

          <Animated.View
            style={[
              styles.modalContainer,
              {
                transform: [{ translateY }],
                opacity: modalAnimation,
              },
            ]}
          >
            <SafeAreaView style={styles.modalContent}>
              {/* Modal Header */}
              <View style={styles.modalHeader}>
                <View style={styles.modalHeaderIcon}>
                  <Icon name="info" size={28} color={GOLD_COLORS.primary} />
                </View>
                <Text style={styles.modalTitle}>Buy USDT</Text>
                <TouchableOpacity
                  style={styles.closeButton}
                  onPress={onClose}
                  hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
                >
                  <Icon name="close" size={24} color="#666666" />
                </TouchableOpacity>
              </View>

              {/* Terms Content */}
              <ScrollView
                style={styles.termsScrollView}
                showsVerticalScrollIndicator={false}
              >
                <View style={styles.termsContent}>
                  <View style={styles.termsSection}>
                    <Icon name="person" size={20} color={GOLD_COLORS.info} />
                    <Text style={styles.termsText}>
                      Dear user, you will pay fiat of your chosen country to our registered merchant's bank account,
                      upload payment proof 🧾, wait for their verification of your payment from their Mobile bank account
                      before releasing the USDT to your TSA Connect virtual account if only your payment is valid.
                    </Text>
                  </View>

                  <View style={styles.merchantNotice}>
                    <Icon name="business" size={20} color={GOLD_COLORS.warning} />
                    <Text style={styles.merchantNoticeText}>
                      PS: Merchant USDT is locked with us. Don't send fake payment or you will be sanctioned!
                    </Text>
                  </View>

                  <View style={styles.securityTips}>
                    <Text style={styles.securityTipsTitle}>Security Guidelines:</Text>
                    <View style={styles.tipItem}>
                      <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                      <Text style={styles.tipText}>Only deposit to verified merchant bank details</Text>
                    </View>
                    <View style={styles.tipItem}>
                      <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                      <Text style={styles.tipText}>Always upload valid payment proof</Text>
                    </View>
                    <View style={styles.tipItem}>
                      <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                      <Text style={styles.tipText}>Wait for merchant confirmation before expecting USDT</Text>
                    </View>
                    <View style={styles.tipItem}>
                      <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                      <Text style={styles.tipText}>Use chat box for communication</Text>
                    </View>
                  </View>

                  <View style={styles.warningBox}>
                    <Icon name="warning" size={24} color={GOLD_COLORS.error} />
                    <Text style={styles.warningBoxText}>
                      ⚠ Sending fake payment will result in account suspension and legal action!
                    </Text>
                  </View>
                </View>
              </ScrollView>

              {/* Action Buttons */}
              <View style={styles.modalActionButtons}>
                <TouchableOpacity
                  style={styles.cancelButton}
                  onPress={onClose}
                >
                  <Text style={styles.cancelButtonText}>Cancel</Text>
                </TouchableOpacity>

                <TouchableOpacity
                  style={styles.proceedButton}
                  onPress={onProceed}
                >
                  <Text style={styles.proceedButtonText}>Understood & Proceed</Text>
                </TouchableOpacity>
              </View>
            </SafeAreaView>
          </Animated.View>
        </Animated.View>
      </KeyboardAvoidingView>
    </Modal>
  );
};

// Chat Modal Component
const ChatModal: React.FC<{
  visible: boolean;
  onClose: () => void;
  merchant: Merchant;
  transaction: Transaction;
  onUploadProof: (imageUri: string) => void;
  onCompleteTransaction: () => void;
}> = ({ visible, onClose, merchant, transaction, onUploadProof, onCompleteTransaction }) => {
  const [messages, setMessages] = useState<ChatMessage[]>([
    {
      id: '1',
      sender: 'system',
      message: 'Welcome to the payment verification chat. Please upload your payment proof for verification.',
      timestamp: new Date(Date.now() - 60000),
      type: 'system'
    },
    {
      id: '2',
      sender: 'merchant',
      message: `Hello! I'm ${merchant.name}. Please upload a clear screenshot of your bank transfer showing ${transaction.amountLocal.toLocaleString()} ${merchant.bankDetails?.currency} sent to my account.`,
      timestamp: new Date(Date.now() - 30000),
      type: 'text'
    }
  ]);
  const [inputMessage, setInputMessage] = useState('');
  const [uploading, setUploading] = useState(false);
  const scrollViewRef = useRef<ScrollView>(null);

  useEffect(() => {
    console.log('ChatModal mounted/updated, visible:', visible);
    console.log('Merchant:', merchant.name);
    console.log('Transaction amount:', transaction.amountLocal);
  }, [visible]);

  useEffect(() => {
    if (scrollViewRef.current) {
      scrollViewRef.current.scrollToEnd({ animated: true });
    }
  }, [messages]);

  const handleSendMessage = () => {
    if (inputMessage.trim()) {
      const newMessage: ChatMessage = {
        id: Date.now().toString(),
        sender: 'user',
        message: inputMessage,
        timestamp: new Date(),
        type: 'text'
      };
      setMessages(prev => [...prev, newMessage]);
      setInputMessage('');

      // Simulate merchant response
      setTimeout(() => {
        const merchantResponse: ChatMessage = {
          id: (Date.now() + 1).toString(),
          sender: 'merchant',
          message: 'Thanks for the message. Please upload your payment proof when ready.',
          timestamp: new Date(),
          type: 'text'
        };
        setMessages(prev => [...prev, merchantResponse]);
      }, 2000);
    }
  };

  const handleUploadImage = () => {
    Alert.alert(
      'Upload Payment Proof',
      'Simulated upload - in production, implement react-native-image-picker',
      [
        {
          text: 'Simulate Upload',
          onPress: () => {
            setUploading(true);

            // Simulate image upload process
            setTimeout(() => {
              const imageUri = 'https://via.placeholder.com/300x200/FFD700/000000?text=Payment+Proof';
              const newMessage: ChatMessage = {
                id: Date.now().toString(),
                sender: 'user',
                message: 'Payment proof uploaded',
                timestamp: new Date(),
                type: 'image',
                imageUri: imageUri
              };

              setMessages(prev => [...prev, newMessage]);
              onUploadProof(imageUri);

              // Simulate merchant verification
              setTimeout(() => {
                const verificationMessage: ChatMessage = {
                  id: (Date.now() + 1).toString(),
                  sender: 'merchant',
                  message: 'Payment proof received. Verifying...',
                  timestamp: new Date(),
                  type: 'text'
                };
                setMessages(prev => [...prev, verificationMessage]);

                setTimeout(() => {
                  const confirmedMessage: ChatMessage = {
                    id: (Date.now() + 2).toString(),
                    sender: 'merchant',
                    message: '✅ Payment verified! USDT has been released to your account.',
                    timestamp: new Date(),
                    type: 'text'
                  };
                  setMessages(prev => [...prev, confirmedMessage]);

                  const systemMessage: ChatMessage = {
                    id: (Date.now() + 3).toString(),
                    sender: 'system',
                    message: 'Transaction completed successfully. You can now close this chat.',
                    timestamp: new Date(),
                    type: 'system'
                  };
                  setMessages(prev => [...prev, systemMessage]);
                }, 3000);
              }, 1000);

              setUploading(false);
            }, 1500);
          }
        },
        {
          text: 'Cancel',
          style: 'cancel'
        }
      ]
    );
  };

  const handleCompleteTransaction = () => {
    Alert.alert(
      'Complete Transaction',
      'Are you sure you want to mark this transaction as completed?',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Yes, Complete',
          onPress: () => {
            onCompleteTransaction();
            onClose();
          }
        },
      ]
    );
  };

  const formatTime = (date: Date) => {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  };

  const renderMessage = (message: ChatMessage) => {
    const isUser = message.sender === 'user';

    return (
      <View
        key={message.id}
        style={[
          styles.messageContainer,
          isUser ? styles.userMessageContainer : styles.otherMessageContainer,
          message.type === 'system' && styles.systemMessageContainer,
        ]}
      >
        {message.type === 'system' && (
          <View style={styles.systemMessage}>
            <Icon name="info" size={16} color={GOLD_COLORS.info} />
            <Text style={styles.systemMessageText}>{message.message}</Text>
          </View>
        )}

        {message.type === 'text' && (
          <>
            <View style={[
              styles.messageBubble,
              isUser ? styles.userMessageBubble : styles.merchantMessageBubble,
            ]}>
              <Text style={[
                styles.messageText,
                isUser ? styles.userMessageText : styles.merchantMessageText,
              ]}>
                {message.message}
              </Text>
            </View>
            <Text style={styles.messageTime}>
              {formatTime(message.timestamp)}
              {isUser ? ' • You' : ` • ${merchant.name}`}
            </Text>
          </>
        )}

        {message.type === 'image' && message.imageUri && (
          <>
            <View style={[
              styles.messageBubble,
              isUser ? styles.userMessageBubble : styles.merchantMessageBubble,
            ]}>
              <ImageBackground
                source={{ uri: message.imageUri }}
                style={styles.messageImage}
                imageStyle={styles.messageImageStyle}
              >
                <View style={styles.imageOverlay}>
                  <Icon name="image" size={24} color="#FFFFFF" />
                  <Text style={styles.imageText}>Payment Proof</Text>
                </View>
              </ImageBackground>
              <Text style={styles.imageCaption}>Payment screenshot uploaded</Text>
            </View>
            <Text style={styles.messageTime}>
              {formatTime(message.timestamp)}
              {isUser ? ' • You' : ` • ${merchant.name}`}
            </Text>
          </>
        )}
      </View>
    );
  };

  return (
    <Modal
      visible={visible}
      animationType="slide"
      onRequestClose={onClose}
      statusBarTranslucent={false}
    >
      <SafeAreaView style={styles.chatContainer}>
        {/* Chat Header */}
        <View style={styles.chatHeader}>
          <TouchableOpacity onPress={onClose} style={styles.backButton}>
            <Icon name="arrow-back" size={24} color="#000" />
          </TouchableOpacity>
          <View style={styles.chatHeaderInfo}>
            <View style={styles.merchantChatAvatar}>
              <Text style={styles.merchantChatAvatarText}>
                {merchant.name.charAt(0).toUpperCase()}
              </Text>
            </View>
            <View>
              <Text style={styles.chatMerchantName}>{merchant.name}</Text>
              <Text style={styles.chatStatus}>
                {messages[messages.length - 1]?.sender === 'merchant' ? 'Online' : 'Last seen recently'}
              </Text>
            </View>
          </View>
          <TouchableOpacity onPress={handleCompleteTransaction} style={styles.completeButton}>
            <Icon name="check-circle" size={24} color={GOLD_COLORS.success} />
          </TouchableOpacity>
        </View>

        {/* Chat Messages */}
        <ScrollView
          ref={scrollViewRef}
          style={styles.chatMessages}
          contentContainerStyle={styles.chatMessagesContent}
        >
          {/* Transaction Info */}
          <View style={styles.transactionInfoCard}>
            <Text style={styles.transactionInfoTitle}>Transaction Details</Text>
            <View style={styles.transactionInfoRow}>
              <Text style={styles.transactionInfoLabel}>Amount:</Text>
              <Text style={styles.transactionInfoValue}>
                {transaction.amountLocal.toLocaleString()} {merchant.bankDetails?.currency}
              </Text>
            </View>
            <View style={styles.transactionInfoRow}>
              <Text style={styles.transactionInfoLabel}>Merchant Rate:</Text>
              <Text style={styles.transactionInfoValue}>
                1 USD = {transaction.merchantExchangeRate.toLocaleString()} {merchant.bankDetails?.currency}
              </Text>
            </View>
            <View style={styles.transactionInfoRow}>
              <Text style={styles.transactionInfoLabel}>To Account:</Text>
              <Text style={styles.transactionInfoValue}>
                {merchant.bankDetails?.accountName}
              </Text>
            </View>
            <View style={styles.transactionInfoRow}>
              <Text style={styles.transactionInfoLabel}>Status:</Text>
              <Text style={[
                styles.transactionInfoValue,
                { color: transaction.status === 'pending' ? GOLD_COLORS.warning : GOLD_COLORS.success }
              ]}>
                {transaction.status.charAt(0).toUpperCase() + transaction.status.slice(1)}
              </Text>
            </View>
          </View>

          {/* Upload Instructions */}
          <View style={styles.uploadInstructions}>
            <Icon name="upload-file" size={20} color={GOLD_COLORS.primary} />
            <Text style={styles.uploadInstructionsText}>
              Upload a clear screenshot showing the transfer of {transaction.amountLocal.toLocaleString()} {merchant.bankDetails?.currency} to {merchant.bankDetails?.accountName}
            </Text>
          </View>

          {/* Messages */}
          {messages.map(renderMessage)}
        </ScrollView>

        {/* Chat Input */}
        <View style={styles.chatInputContainer}>
          <TouchableOpacity
            style={styles.uploadButton}
            onPress={handleUploadImage}
            disabled={uploading}
          >
            <Icon
              name={uploading ? "hourglass-empty" : "attach-file"}
              size={24}
              color={uploading ? "#999" : GOLD_COLORS.primary}
            />
            <Text style={styles.uploadButtonText}>
              {uploading ? 'Uploading...' : 'Upload Proof'}
            </Text>
          </TouchableOpacity>

          <View style={styles.messageInputContainer}>
            <TextInput
              style={styles.messageInput}
              value={inputMessage}
              onChangeText={setInputMessage}
              placeholder="Type a message..."
              placeholderTextColor="#999"
              multiline
            />
            <TouchableOpacity
              style={styles.sendButton}
              onPress={handleSendMessage}
              disabled={!inputMessage.trim()}
            >
              <Icon name="send" size={20} color={inputMessage.trim() ? GOLD_COLORS.primary : "#CCC"} />
            </TouchableOpacity>
          </View>
        </View>
      </SafeAreaView>
    </Modal>
  );
};

// Main DepositScreen Component
const DepositScreen: React.FC = () => {
  // State for deposit steps
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [showTermsModal, setShowTermsModal] = useState(false);
  const [showChatModal, setShowChatModal] = useState(false);

  // Selection state
  const [selectedCountry, setSelectedCountry] = useState<string>('US');
  const [selectedCurrency, setSelectedCurrency] = useState<string>('USD');
  const [usdAmount, setUsdAmount] = useState<string>('100');
  const [selectedMerchant, setSelectedMerchant] = useState<Merchant | null>(null);
  const [transaction, setTransaction] = useState<Transaction | null>(null);

  // Debug logs
  useEffect(() => {
    console.log('=== DEBUG LOGS ===');
    console.log('Current Step:', step);
    console.log('Selected Merchant:', selectedMerchant?.name);
    console.log('Transaction:', transaction);
    console.log('Show Chat Modal:', showChatModal);
    console.log('USD Amount:', usdAmount);
    console.log('=================');
  }, [step, selectedMerchant, transaction, showChatModal, usdAmount]);

  // Log when showChatModal changes
  useEffect(() => {
    console.log('showChatModal changed to:', showChatModal);
  }, [showChatModal]);

  // Update currency when country changes
  useEffect(() => {
    const country = COUNTRIES_DATA.find(c => c.code === selectedCountry);
    if (country) {
      setSelectedCurrency(country.currency);

      if (selectedMerchant && selectedMerchant.bankDetails?.country !== selectedCountry) {
        setSelectedMerchant(null);
      }
    }
  }, [selectedCountry]);

  // Create a dummy transaction function
  const createDummyTransaction = (merchant: Merchant): Transaction => {
    const amountUSD = parseFloat(usdAmount) || 100;
    const localAmount = amountUSD * merchant.exchangeRate;
    const usdtAmount = amountUSD * 0.99;

    return {
      id: Date.now().toString(),
      merchantId: merchant.id,
      amountUSD: amountUSD,
      amountLocal: localAmount,
      usdtAmount: usdtAmount,
      status: 'pending',
      timestamp: new Date(),
      merchantExchangeRate: merchant.exchangeRate,
    };
  };

  // Calculate local currency amount based on merchant's exchange rate
  const calculateLocalAmount = (merchantRate?: number): number => {
    const amount = parseFloat(usdAmount) || 0;
    const exchangeRate = merchantRate || selectedMerchant?.exchangeRate || 0;
    return amount * exchangeRate;
  };

  // Calculate USDT amount
  const calculateUSDTAmount = (): number => {
    const amount = parseFloat(usdAmount) * 0.99 || 0;
    return amount;
  };

  // Handle buy button press
  const handleBuyPress = () => {
    setShowTermsModal(true);
  };

  // Proceed after understanding terms
  const handleProceed = () => {
    setShowTermsModal(false);
    setStep(1);
  };

  // Handle next step
  const handleNextStep = () => {
    console.log('handleNextStep called. Current step:', step);

    if (step === 1) {
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

      console.log('Moving to Step 2');
      setStep(2);
    } else if (step === 2) {
      if (!selectedMerchant) {
        Alert.alert('Error', 'Please select a merchant');
        return;
      }

      const dummyTransaction = createDummyTransaction(selectedMerchant);
      console.log('Creating dummy transaction:', dummyTransaction);

      setTransaction(dummyTransaction);

      // Wait a bit for state to update before moving to next step
      setTimeout(() => {
        setStep(3);
      }, 50);
    }
  };

  // Enhanced handlePaid function with chat - FIXED
  const handlePaid = () => {
    console.log('handlePaid called');
    console.log('Selected Merchant:', selectedMerchant);
    console.log('Current Transaction:', transaction);

    if (!selectedMerchant) {
      Alert.alert('Error', 'Please select a merchant first');
      return;
    }

    // Always create a new transaction when Paid is clicked
    const dummyTransaction = createDummyTransaction(selectedMerchant);
    console.log('Creating transaction for chat:', dummyTransaction);

    // Set the transaction first
    setTransaction(dummyTransaction);

    // Wait for state to update, then show confirmation
    setTimeout(() => {
      Alert.alert(
        'Confirm Payment',
        `Have you made the payment of ${dummyTransaction.amountLocal.toLocaleString()} ${selectedCurrency} to ${selectedMerchant.name}'s bank account?`,
        [
          { text: 'Not Yet', style: 'cancel' },
          {
            text: 'Yes, I Have Paid',
            onPress: () => {
              console.log('User confirmed payment, opening chat modal...');
              console.log('Transaction exists:', !!dummyTransaction);
              console.log('Merchant exists:', !!selectedMerchant);

              // Ensure transaction is set before opening modal
              setTransaction(dummyTransaction);

              // Small delay to ensure state update
              setTimeout(() => {
                console.log('Setting showChatModal to true');
                setShowChatModal(true);
              }, 100);
            }
          },
        ]
      );
    }, 100);
  };

  // Handle payment proof upload
  const handleUploadProof = (imageUri: string) => {
    console.log('Payment proof uploaded:', imageUri);

    // Update transaction status
    if (transaction) {
      setTransaction({
        ...transaction,
        status: 'processing'
      });
    }
  };

  // Handle transaction completion
  const handleCompleteTransaction = () => {
    if (transaction) {
      setTransaction({
        ...transaction,
        status: 'completed'
      });

      Alert.alert(
        'Transaction Completed',
        'Your USDT has been released to your account!',
        [
          {
            text: 'OK',
            onPress: () => {
              // Reset the flow
              setStep(1);
              setSelectedMerchant(null);
              setTransaction(null);
              setShowChatModal(false);
            }
          },
        ]
      );
    }
  };

  // Handle report
  const handleReport = () => {
    Alert.alert(
      'Report Dispute',
      'Please describe the issue you\'re experiencing with this transaction.',
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Submit Report', onPress: () => console.log('Report submitted') },
      ]
    );
  };

  // Handle chat from step 3 - FIXED
  const handleChat = () => {
    console.log('Chat button clicked from step 3');
    console.log('Selected Merchant:', selectedMerchant);
    console.log('Transaction:', transaction);

    if (selectedMerchant) {
      // Ensure we have a transaction
      let currentTransaction = transaction;
      if (!currentTransaction) {
        currentTransaction = createDummyTransaction(selectedMerchant);
        console.log('Created new transaction for chat:', currentTransaction);
        setTransaction(currentTransaction);
      }

      console.log('Opening chat modal from step 3');

      // Small delay to ensure state is updated
      setTimeout(() => {
        setShowChatModal(true);
      }, 100);
    } else {
      Alert.alert('Error', 'No merchant selected. Please go back and select a merchant.');
    }
  };

  // Test chat modal function (for debugging)
  const testChatModal = () => {
    console.log('Testing chat modal...');

    // Use the first merchant as dummy
    const testMerchant = merchants[0];
    const testTransaction = createDummyTransaction(testMerchant);

    setSelectedMerchant(testMerchant);
    setTransaction(testTransaction);

    setTimeout(() => {
      console.log('Opening test chat modal');
      setShowChatModal(true);
    }, 100);
  };

  // Available merchants with their own exchange rates and bank details
  const [merchants] = useState<Merchant[]>([
    {
      id: '1',
      name: 'GoldTrust Exchange',
      rating: 4.9,
      completedTrades: 1245,
      minAmount: 50,
      maxAmount: 5000,
      responseTime: '5-15 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
      exchangeRate: 1.00, // Merchant sets their own rate
      bankDetails: {
        bankName: 'Chase Bank',
        accountNumber: '9876543210',
        accountName: 'GoldTrust Exchange LLC',
        country: 'US',
        currency: 'USD',
      },
    },
    {
      id: '2',
      name: 'Premium US Exchange',
      rating: 4.8,
      completedTrades: 876,
      minAmount: 100,
      maxAmount: 3000,
      responseTime: '10-20 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
      exchangeRate: 0.99, // Different merchant, different rate
      bankDetails: {
        bankName: 'Bank of America',
        accountNumber: '1234567890',
        accountName: 'Premium US Exchange Inc',
        country: 'US',
        currency: 'USD',
      },
    },
    {
      id: '3',
      name: 'QuickCash Merchant',
      rating: 4.7,
      completedTrades: 567,
      minAmount: 20,
      maxAmount: 1000,
      responseTime: 'Instant',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'basic',
      exchangeRate: 1500.00, // Merchant sets their own NGN rate
      bankDetails: {
        bankName: 'GTBank',
        accountNumber: '0123456789',
        accountName: 'QuickCash Merchant',
        country: 'NG',
        currency: 'NGN',
      },
    },
    {
      id: '4',
      name: 'Nigeria Best Rate',
      rating: 4.6,
      completedTrades: 432,
      minAmount: 50,
      maxAmount: 2000,
      responseTime: '5-10 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
      exchangeRate: 1520.00, // Different merchant, better rate
      bankDetails: {
        bankName: 'Zenith Bank',
        accountNumber: '9876543210',
        accountName: 'Nigeria Best Rate Ltd',
        country: 'NG',
        currency: 'NGN',
      },
    },
    {
      id: '5',
      name: 'UK Secure Trade',
      rating: 4.8,
      completedTrades: 892,
      minAmount: 50,
      maxAmount: 3000,
      responseTime: '10-30 mins',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
      exchangeRate: 0.75, // Merchant sets their own GBP rate
      bankDetails: {
        bankName: 'Barclays Bank',
        accountNumber: '1234567890',
        accountName: 'UK Secure Trade Ltd',
        country: 'UK',
        currency: 'GBP',
      },
    },
    {
      id: '6',
      name: 'London Forex',
      rating: 4.9,
      completedTrades: 1123,
      minAmount: 100,
      maxAmount: 5000,
      responseTime: 'Instant',
      paymentMethods: ['bank-transfer'],
      verificationLevel: 'high',
      exchangeRate: 0.74, // Competitive rate
      bankDetails: {
        bankName: 'HSBC',
        accountNumber: '5555666677',
        accountName: 'London Forex Exchange',
        country: 'UK',
        currency: 'GBP',
      },
    },
  ]);

  // Get merchants filtered by selected country
  const getFilteredMerchants = () => {
    return merchants.filter(merchant =>
      merchant.bankDetails?.country === selectedCountry
    );
  };

  // Get average exchange rate for the country (for display purposes only)
  const getAverageExchangeRate = (): number => {
    const filtered = getFilteredMerchants();
    if (filtered.length === 0) return 0;

    const sum = filtered.reduce((acc, merchant) => acc + merchant.exchangeRate, 0);
    return sum / filtered.length;
  };

  // Render step 1: Country and amount selection
  const renderStep1 = () => {
    const averageRate = getAverageExchangeRate();
    const hasMerchants = getFilteredMerchants().length > 0;

    return (
      <View style={styles.stepContainer}>
        <Text style={styles.stepTitle}>Buy USDT</Text>

        {/* Country Selection */}
        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Select Country</Text>
          <Text style={styles.sectionSubtitle}>Choose your preferred country</Text>

          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            style={styles.countryScroll}
          >
            {COUNTRIES_DATA.map((country) => {
              const countryMerchants = merchants.filter(m => m.bankDetails?.country === country.code);
              const merchantCount = countryMerchants.length;
              const avgRate = merchantCount > 0 ?
                (countryMerchants.reduce((acc, m) => acc + m.exchangeRate, 0) / merchantCount).toLocaleString() :
                'No merchants';

              return (
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
                  <Text style={styles.exchangeRateBadge}>
                    {merchantCount} merchants
                  </Text>
                  {merchantCount > 0 && (
                    <Text style={styles.avgRateBadge}>
                      Avg: {avgRate}
                    </Text>
                  )}
                </TouchableOpacity>
              );
            })}
          </ScrollView>
        </View>

        {/* Amount Input */}
        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Enter Amount to Buy</Text>

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
              <Text style={styles.conversionLabel}>Available in {selectedCountry}:</Text>
              <Text style={styles.conversionAmount}>
                {getFilteredMerchants().length} merchants
              </Text>
              {hasMerchants && (
                <>
                  <Text style={styles.conversionLabel}>Average Merchant Rate:</Text>
                  <Text style={styles.conversionAmount}>
                    ~{averageRate.toLocaleString()} {selectedCurrency}/USDT
                  </Text>
                </>
              )}
            </View>

            <Text style={styles.amountNote}>
              You will receive: {calculateUSDTAmount().toFixed(2)} USDT
            </Text>
            <Text style={styles.feeNote}>
              Fee 1% (0.5% to Merchant and 0.5% System Service)
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

        {/* Merchant Availability Notice */}
        {!hasMerchants && (
          <View style={styles.warningAlert}>
            <Icon name="warning" size={20} color={GOLD_COLORS.warning} />
            <Text style={styles.warningAlertText}>
              No merchants available for {COUNTRIES_DATA.find(c => c.code === selectedCountry)?.name}. Please select a different country.
            </Text>
          </View>
        )}
      </View>
    );
  };

  // Render step 2: Merchant selection
  const renderStep2 = () => {
    const filteredMerchants = getFilteredMerchants();
    const amount = parseFloat(usdAmount) || 0;
    const availableMerchants = filteredMerchants.filter(m =>
      amount >= m.minAmount && amount <= m.maxAmount
    );

    return (
      <View style={styles.stepContainer}>
        <Text style={styles.stepTitle}>Select Merchant</Text>
        <Text style={styles.stepSubtitle}>
          Available merchants for ${usdAmount} in {COUNTRIES_DATA.find(c => c.code === selectedCountry)?.name}
        </Text>

        <View style={styles.filterSection}>
          <View style={styles.filterItem}>
            <Icon name="filter-list" size={18} color="#666" />
            <Text style={styles.filterText}>Sort by: Best Rate</Text>
          </View>
          <View style={styles.filterItem}>
            <Icon name="currency-exchange" size={18} color="#666" />
            <Text style={styles.filterText}>Merchants: {availableMerchants.length}</Text>
          </View>
        </View>

        <ScrollView style={styles.merchantList}>
          {availableMerchants.length > 0 ? (
            availableMerchants.map((merchant) => (
              <MerchantCard
                key={merchant.id}
                merchant={merchant}
                isSelected={selectedMerchant?.id === merchant.id}
                onSelect={setSelectedMerchant}
                showDetails={true}
                selectedCurrency={selectedCurrency}
              />
            ))
          ) : (
            <View style={styles.noMerchantsContainer}>
              <Icon name="business" size={48} color="#CCCCCC" />
              <Text style={styles.noMerchantsText}>
                No merchants available for {COUNTRIES_DATA.find(c => c.code === selectedCountry)?.name} with amount ${usdAmount || '0'}
              </Text>
              <Text style={styles.noMerchantsSubtext}>
                Try adjusting the amount or select a different country
              </Text>
            </View>
          )}
        </ScrollView>

        {/* Selected Merchant Summary */}
        {selectedMerchant && selectedMerchant.bankDetails && (
          <View style={styles.selectedSummary}>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Merchant Rate:</Text>
              <Text style={styles.summaryValue}>
                {selectedMerchant.exchangeRate.toLocaleString()} {selectedCurrency}/USDT
              </Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>You Will Pay:</Text>
              <Text style={styles.summaryValue}>
                {calculateLocalAmount(selectedMerchant.exchangeRate).toLocaleString()} {selectedCurrency}
              </Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>You Will Receive:</Text>
              <Text style={styles.summaryValue}>
                {calculateUSDTAmount().toFixed(2)} USDT
              </Text>
            </View>
          </View>
        )}

        {/* Merchant Bank Details */}
        {selectedMerchant && selectedMerchant.bankDetails && (
          <BankDetailsDisplay
            bankDetails={selectedMerchant.bankDetails}
            amountLocal={calculateLocalAmount(selectedMerchant.exchangeRate)}
            exchangeRate={selectedMerchant.exchangeRate}
          />
        )}
      </View>
    );
  };

  // Render step 3: Confirm payment
  const renderStep3 = () => {
    return (
      <View style={styles.stepContainer}>
        <Text style={styles.stepTitle}>Confirm Payment</Text>

        {selectedMerchant && selectedMerchant.bankDetails ? (
          <>
            {/* Transaction Summary */}
            <View style={styles.transactionSummary}>
              <View style={styles.summaryHeader}>
                <Text style={styles.summaryTitle}>Transaction Details</Text>
                <View style={styles.statusBadge}>
                  <Text style={styles.statusText}>Awaiting Payment</Text>
                </View>
              </View>

              <View style={styles.transactionDetailRow}>
                <Text style={styles.detailLabel}>Merchant:</Text>
                <Text style={styles.detailValue}>{selectedMerchant.name}</Text>
              </View>

              <View style={styles.transactionDetailRow}>
                <Text style={styles.detailLabel}>Buying Amount:</Text>
                <Text style={styles.detailValue}>{calculateUSDTAmount().toFixed(2)} USDT</Text>
              </View>

              <View style={styles.transactionDetailRow}>
                <Text style={styles.detailLabel}>Amount to Pay:</Text>
                <Text style={[styles.detailValue, styles.paymentAmount]}>
                  {calculateLocalAmount(selectedMerchant.exchangeRate).toLocaleString()} {selectedCurrency}
                </Text>
              </View>

              <View style={styles.transactionDetailRow}>
                <Text style={styles.detailLabel}>Merchant Exchange Rate:</Text>
                <Text style={styles.detailValue}>
                  1 USD = {selectedMerchant.exchangeRate.toLocaleString()} {selectedCurrency}
                </Text>
              </View>

              <View style={styles.divider} />

              <View style={styles.totalRow}>
                <Text style={styles.totalLabel}>Total USDT:</Text>
                <Text style={styles.totalValue}>{calculateUSDTAmount().toFixed(2)} USDT</Text>
              </View>
            </View>

            {/* Bank Details for Payment */}
            <BankDetailsDisplay
              bankDetails={selectedMerchant.bankDetails}
              amountLocal={calculateLocalAmount(selectedMerchant.exchangeRate)}
              exchangeRate={selectedMerchant.exchangeRate}
            />

            {/* Payment Instructions */}
            <View style={styles.paymentInstructions}>
              <Text style={styles.instructionsTitle}>Payment Instructions:</Text>
              <View style={styles.instructionStep}>
                <View style={styles.stepNumberCircle}>
                  <Text style={styles.stepNumberText}>1</Text>
                </View>
                <Text style={styles.instructionText}>
                  Deposit exactly {calculateLocalAmount(selectedMerchant.exchangeRate).toLocaleString()} {selectedCurrency} to the merchant's bank account
                </Text>
              </View>
              <View style={styles.instructionStep}>
                <View style={styles.stepNumberCircle}>
                  <Text style={styles.stepNumberText}>2</Text>
                </View>
                <Text style={styles.instructionText}>
                  Take a screenshot of successful payment (payment proof)
                </Text>
              </View>
              <View style={styles.instructionStep}>
                <View style={styles.stepNumberCircle}>
                  <Text style={styles.stepNumberText}>3</Text>
                </View>
                <Text style={styles.instructionText}>
                  Click "Chat Box" to upload payment proof to merchant
                </Text>
              </View>
              <View style={styles.instructionStep}>
                <View style={styles.stepNumberCircle}>
                  <Text style={styles.stepNumberText}>4</Text>
                </View>
                <Text style={styles.instructionText}>
                  Wait for merchant to verify and release USDT to your account
                </Text>
              </View>
            </View>

            {/* Warning Box */}
            <View style={styles.finalWarning}>
              <Icon name="warning" size={24} color={GOLD_COLORS.error} />
              <Text style={styles.finalWarningText}>
                ⚠ Merchant USDT is locked with TSA Connect. Only valid payments will be processed!
              </Text>
            </View>

            {/* Action Buttons */}
            <View style={styles.bottomActionButtons}>
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
            </View>

            {/* General Notices */}
            <View style={styles.generalNotices}>
              <View style={styles.noticeItem}>
                <Icon name="message" size={16} color={GOLD_COLORS.info} />
                <Text style={styles.noticeText}>
                  Click "chat box" to upload payment proof for merchant to confirm
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
                  Merchant USDT is locked until payment is verified
                </Text>
              </View>
            </View>
          </>
        ) : (
          <View style={styles.errorContainer}>
            <Icon name="error" size={48} color={GOLD_COLORS.error} />
            <Text style={styles.errorText}>Transaction data not available</Text>
            <Text style={styles.errorSubtext}>Please go back and select a merchant</Text>
            <TouchableOpacity
              style={styles.backButtonError}
              onPress={() => setStep(2)}
            >
              <Text style={styles.backButtonErrorText}>Go Back to Step 2</Text>
            </TouchableOpacity>
          </View>
        )}
      </View>
    );
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
                    <Text style={styles.stepNumberText}>{stepNum}</Text>
                  )}
                </View>
                <Text style={styles.stepLabel}>
                  {stepNum === 1 ? 'Amount' : stepNum === 2 ? 'Merchant' : 'Confirm'}
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
                onPress={() => step > 1 ? setStep(step - 1 as 1 | 2 | 3) : setShowTermsModal(true)}
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
                onPress={step === 2 ? handlePaid : handleNextStep}
                disabled={(step === 1 && !usdAmount) || (step === 2 && !selectedMerchant)}
              >
                <Text style={styles.nextButtonText}>
                  {step === 1 ? 'Search 🔍' : 'Paid'}
                </Text>
                {step === 2 && <Icon name="payment" size={20} color="#000" />}
              </TouchableOpacity>
            </View>
          )}

          {/* Debug Button - Only in development */}
          {__DEV__ && (
            <TouchableOpacity
              style={[styles.nextButton, { backgroundColor: '#FF0000', marginTop: 10, marginHorizontal: 20 }]}
              onPress={testChatModal}
            >
              <Text style={styles.nextButtonText}>DEBUG: Test Chat Modal</Text>
            </TouchableOpacity>
          )}
        </ScrollView>

        {/* Terms Modal */}
        <TermsModal
          visible={showTermsModal}
          onClose={() => setShowTermsModal(false)}
          onProceed={handleProceed}
        />

        {/* Chat Modal - Always render but control visibility */}
        {selectedMerchant && transaction && (
          <ChatModal
            visible={showChatModal}
            onClose={() => {
              console.log('Closing chat modal');
              setShowChatModal(false);
            }}
            merchant={selectedMerchant}
            transaction={transaction}
            onUploadProof={handleUploadProof}
            onCompleteTransaction={handleCompleteTransaction}
          />
        )}
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

// Complete Styles for DepositScreen.tsx
const styles = StyleSheet.create({
  // Main container styles
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  scrollContainer: {
    flex: 1,
  },

  // Header styles
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 10 : 20,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  helpButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 12,
  },
  buyButtonText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },

  // Step indicator styles
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
  stepNumberText: {
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

  // Step container styles
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

  // Section styles
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

  // Country selection styles
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
  exchangeRateBadge: {
    fontSize: 10,
    color: GOLD_COLORS.dark,
    marginTop: 4,
    fontWeight: '600',
    backgroundColor: 'rgba(255, 215, 0, 0.1)',
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 4,
  },
  avgRateBadge: {
    fontSize: 10,
    color: GOLD_COLORS.success,
    marginTop: 2,
    fontWeight: '600',
  },

  // Amount input styles
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
  feeNote: {
    fontSize: 12,
    color: '#666666',
    marginTop: 8,
    fontStyle: 'italic',
  },

  // Quick amount buttons
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

  // Filter section styles
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

  // Merchant list styles
  merchantList: {
    maxHeight: 400,
  },

  // Merchant card styles
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

  // Rating styles
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

  // Verification badge
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

  // Merchant details
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
  merchantBankPreview: {
    marginTop: 12,
    paddingTop: 12,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  bankPreviewTitle: {
    fontSize: 13,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 4,
  },
  bankPreviewAccount: {
    fontSize: 12,
    color: '#666666',
  },

  // Selected summary styles
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

  // Bank details container
  bankDetailsContainer: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    marginTop: 16,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  bankDetailsHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  bankDetailsTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 12,
  },
  warningAlert: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF5E6',
    padding: 12,
    borderRadius: 8,
    marginBottom: 16,
  },
  warningAlertText: {
    fontSize: 12,
    fontWeight: '600',
    color: GOLD_COLORS.warning,
    marginLeft: 8,
    flex: 1,
  },
  bankInfoCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
  },
  bankInfoRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
    minHeight: 32,
  },
  bankInfoLabel: {
    fontSize: 14,
    color: '#666666',
    width: 120,
  },
  bankInfoValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
    flex: 1,
  },
  copyableRow: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  copyButtonSmall: {
    marginLeft: 8,
    padding: 4,
  },
  countryBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(255, 215, 0, 0.2)',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 8,
  },
  flagEmoji: {
    fontSize: 14,
    marginRight: 4,
  },
  countryText: {
    fontSize: 12,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
  },
  currencyHighlight: {
    fontWeight: '800',
    color: GOLD_COLORS.dark,
  },
  amountHighlight: {
    fontSize: 16,
    fontWeight: '900',
    color: GOLD_COLORS.success,
  },
  securityNote: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#E8F5E9',
    padding: 12,
    borderRadius: 8,
  },
  securityNoteText: {
    fontSize: 12,
    color: GOLD_COLORS.success,
    marginLeft: 8,
    flex: 1,
    fontWeight: '600',
  },

  // Transaction summary styles
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
  transactionDetailRow: {
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
  paymentAmount: {
    fontSize: 16,
    fontWeight: '800',
    color: GOLD_COLORS.error,
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

  // Payment instructions
  paymentInstructions: {
    backgroundColor: '#E8F5E9',
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
  },
  instructionsTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 16,
  },
  instructionStep: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 12,
  },
  stepNumberCircle: {
    width: 24,
    height: 24,
    borderRadius: 12,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
    marginTop: 2,
  },
  instructionText: {
    fontSize: 13,
    color: '#666666',
    flex: 1,
    lineHeight: 18,
  },

  // Warning box
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

  // Action buttons
  bottomActionButtons: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    gap: 12,
    marginBottom: 20,
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

  // General notices
  generalNotices: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
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
    lineHeight: 16,
  },

  // Navigation buttons
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

  // Error container styles
  errorContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    padding: 40,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    marginVertical: 20,
  },
  errorText: {
    fontSize: 18,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginTop: 16,
    marginBottom: 8,
    textAlign: 'center',
  },
  errorSubtext: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    marginBottom: 20,
  },
  backButtonError: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 12,
  },
  backButtonErrorText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
  },

  // No merchants container
  noMerchantsContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    padding: 40,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    marginVertical: 20,
  },
  noMerchantsText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
    textAlign: 'center',
    marginTop: 16,
    marginBottom: 8,
  },
  noMerchantsSubtext: {
    fontSize: 14,
    color: '#999999',
    textAlign: 'center',
  },

  // Modal styles
  modalOverlay: {
    flex: 1,
    justifyContent: 'flex-end',
  },
  modalBackdrop: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
  },
  modalContainer: {
    backgroundColor: '#FFFFFF',
    borderTopLeftRadius: 24,
    borderTopRightRadius: 24,
    maxHeight: '90%',
    minHeight: '70%',
  },
  modalContent: {
    flex: 1,
  },
  modalHeader: {
    padding: 24,
    paddingBottom: 16,
    alignItems: 'center',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
    position: 'relative',
  },
  modalHeaderIcon: {
    backgroundColor: GOLD_COLORS.light,
    width: 60,
    height: 60,
    borderRadius: 30,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  modalTitle: {
    fontSize: 24,
    fontWeight: '900',
    color: '#000000',
    textAlign: 'center',
    marginBottom: 8,
  },
  closeButton: {
    position: 'absolute',
    top: 24,
    right: 24,
    padding: 4,
  },

  // Terms modal styles
  termsScrollView: {
    flex: 1,
    paddingHorizontal: 20,
  },
  termsContent: {
    paddingVertical: 20,
  },
  termsSection: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 20,
  },
  termsText: {
    fontSize: 15,
    color: '#666666',
    marginLeft: 12,
    flex: 1,
    lineHeight: 22,
  },
  merchantNotice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#FFF5E6',
    padding: 16,
    borderRadius: 12,
    marginBottom: 20,
  },
  merchantNoticeText: {
    fontSize: 14,
    color: GOLD_COLORS.warning,
    marginLeft: 12,
    flex: 1,
    fontWeight: '600',
    lineHeight: 20,
  },
  securityTips: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
  },
  securityTipsTitle: {
    fontSize: 16,
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
    lineHeight: 18,
  },
  warningBox: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFE5E5',
    padding: 16,
    borderRadius: 12,
  },
  warningBoxText: {
    fontSize: 14,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginLeft: 12,
    flex: 1,
    lineHeight: 20,
  },
  modalActionButtons: {
    flexDirection: 'row',
    gap: 12,
    padding: 20,
    paddingTop: 16,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  cancelButton: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    padding: 16,
    borderRadius: 12,
    borderWidth: 2,
    borderColor: '#E5E5EA',
    backgroundColor: '#FFFFFF',
  },
  cancelButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#666666',
  },
  proceedButton: {
    flex: 2,
    alignItems: 'center',
    justifyContent: 'center',
    padding: 16,
    borderRadius: 12,
    backgroundColor: GOLD_COLORS.primary,
  },
  proceedButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },

  // Chat Modal Styles
  chatContainer: {
    flex: 1,
    backgroundColor: '#F5F5F5',
  },
  chatHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: '#FFFFFF',
    borderBottomWidth: 1,
    borderBottomColor: '#E0E0E0',
  },
  backButton: {
    padding: 8,
  },
  chatHeaderInfo: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    marginLeft: 12,
  },
  merchantChatAvatar: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  merchantChatAvatarText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
  },
  chatMerchantName: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  chatStatus: {
    fontSize: 12,
    color: '#666666',
  },
  completeButton: {
    padding: 8,
  },
  chatMessages: {
    flex: 1,
  },
  chatMessagesContent: {
    padding: 16,
  },
  transactionInfoCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
  },
  transactionInfoTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  transactionInfoRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  transactionInfoLabel: {
    fontSize: 14,
    color: '#666666',
  },
  transactionInfoValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  uploadInstructions: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#E8F5E9',
    padding: 12,
    borderRadius: 8,
    marginBottom: 16,
  },
  uploadInstructionsText: {
    fontSize: 14,
    color: GOLD_COLORS.success,
    marginLeft: 12,
    flex: 1,
    fontWeight: '600',
  },
  messageContainer: {
    marginBottom: 16,
  },
  userMessageContainer: {
    alignItems: 'flex-end',
  },
  otherMessageContainer: {
    alignItems: 'flex-start',
  },
  systemMessageContainer: {
    alignItems: 'center',
    marginVertical: 8,
  },
  systemMessage: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(90, 200, 250, 0.1)',
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 20,
  },
  systemMessageText: {
    fontSize: 12,
    color: GOLD_COLORS.info,
    marginLeft: 8,
    fontWeight: '600',
  },
  messageBubble: {
    maxWidth: '80%',
    borderRadius: 18,
    padding: 12,
    marginBottom: 4,
  },
  userMessageBubble: {
    backgroundColor: GOLD_COLORS.primary,
    borderBottomRightRadius: 4,
  },
  merchantMessageBubble: {
    backgroundColor: '#FFFFFF',
    borderBottomLeftRadius: 4,
    borderWidth: 1,
    borderColor: '#E0E0E0',
  },
  messageText: {
    fontSize: 14,
    lineHeight: 20,
  },
  userMessageText: {
    color: '#000000',
    fontWeight: '500',
  },
  merchantMessageText: {
    color: '#000000',
  },
  messageTime: {
    fontSize: 11,
    color: '#999999',
    marginTop: 2,
  },
  messageImage: {
    width: 200,
    height: 150,
    borderRadius: 12,
    overflow: 'hidden',
    justifyContent: 'center',
    alignItems: 'center',
  },
  messageImageStyle: {
    borderRadius: 12,
  },
  imageOverlay: {
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    padding: 8,
    borderRadius: 8,
    alignItems: 'center',
  },
  imageText: {
    fontSize: 12,
    color: '#FFFFFF',
    marginTop: 4,
    fontWeight: '600',
  },
  imageCaption: {
    fontSize: 12,
    color: '#666666',
    marginTop: 4,
    textAlign: 'center',
  },
  chatInputContainer: {
    padding: 16,
    backgroundColor: '#FFFFFF',
    borderTopWidth: 1,
    borderTopColor: '#E0E0E0',
  },
  uploadButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingVertical: 12,
    borderRadius: 12,
    marginBottom: 12,
  },
  uploadButtonText: {
    fontSize: 14,
    fontWeight: '600',
    color: GOLD_COLORS.primary,
    marginLeft: 8,
  },
  messageInputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F5F5F5',
    borderRadius: 24,
    paddingHorizontal: 16,
    paddingVertical: 8,
  },
  messageInput: {
    flex: 1,
    fontSize: 14,
    color: '#000000',
    maxHeight: 100,
    paddingVertical: 8,
  },
  sendButton: {
    padding: 8,
  },
});

export default DepositScreen;