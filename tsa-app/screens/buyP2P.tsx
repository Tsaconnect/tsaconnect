// app/screens/P2PBuyScreen.tsx
import { router } from 'expo-router';
import React, { useState } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  TextInput,
  Alert,
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

const P2PBuyScreen: React.FC = () => {
  const [amount, setAmount] = useState('');
  const [rate, setRate] = useState('1500'); // Default rate: $1 = #1500
  const [limitRange, setLimitRange] = useState('');
  const [showConfirmModal, setShowConfirmModal] = useState(false);

  const handleUpload = () => {
    if (!amount || !rate || !limitRange) {
      Alert.alert('Error', 'Please fill all required fields');
      return;
    }

    const amountNum = parseFloat(amount);
    const rateNum = parseFloat(rate);
    const limitNum = parseFloat(limitRange);

    if (amountNum <= 0 || rateNum <= 0 || limitNum <= 0) {
      Alert.alert('Error', 'Please enter valid positive numbers');
      return;
    }

    if (limitNum < amountNum) {
      Alert.alert('Error', 'Limit range must be greater than or equal to amount');
      return;
    }

    setShowConfirmModal(true);
  };

  const handleConfirmUpload = () => {
    setShowConfirmModal(false);
    Alert.alert(
      'Ad Uploaded Successfully!',
      'Your USDT has been locked for 30 days. Your ad will remain active until locked funds are exhausted.',
      [
        {
          text: 'View Marketplace',
          onPress: () => router.push('/marketplace'),
        },
        {
          text: 'Continue',
        },
      ]
    );
  };

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
            <Text style={styles.modalTitle}>Confirm Ad Upload</Text>
            <TouchableOpacity
              onPress={() => setShowConfirmModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <View style={styles.modalBody}>
            <Text style={styles.confirmText}>
              Are you sure you want to upload this ad? Your USDT will be locked for 30 days.
            </Text>

            <View style={styles.detailsCard}>
              <Text style={styles.detailsTitle}>Ad Details:</Text>

              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Amount:</Text>
                <Text style={styles.detailValue}>{amount} USDT</Text>
              </View>

              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Rate:</Text>
                <Text style={styles.detailValue}>$1 = #{rate}</Text>
              </View>

              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Limit Range:</Text>
                <Text style={styles.detailValue}>#{limitRange}</Text>
              </View>

              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Total Value:</Text>
                <Text style={styles.detailValue}>
                  #{(parseFloat(amount) * parseFloat(rate)).toLocaleString()}
                </Text>
              </View>
            </View>

            <View style={styles.warningBox}>
              <Icon name="warning" size={20} color={GOLD_COLORS.dark} />
              <Text style={styles.warningText}>
                Note: Your USDT will be locked for 30 days. Funds will be released if not bought within this period.
              </Text>
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
              onPress={handleConfirmUpload}
              activeOpacity={0.8}
            >
              <Icon name="cloud-upload" size={20} color="#000000" />
              <Text style={styles.confirmButtonText}>Upload Ad</Text>
            </TouchableOpacity>
          </View>
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
          <Text style={styles.headerTitle}>P2P Buy (Internal Seller)</Text>
        </View>

        {/* Main Content */}
        <View style={styles.content}>
          <Text style={styles.sectionTitle}>
            Create Buy Ad (USDT payment for Fiat)
          </Text>

          <Text style={styles.description}>
            As an internal seller, you're offering to sell USDT for fiat currency.
            Buyers will pay you fiat, and you'll release USDT after verifying payment proof.
          </Text>

          {/* Form */}
          <View style={styles.formContainer}>
            {/* Amount Field */}
            <View style={styles.formGroup}>
              <Text style={styles.formLabel}>Enter amount (USDT):</Text>
              <View style={styles.inputContainer}>
                <TextInput
                  style={styles.input}
                  value={amount}
                  onChangeText={setAmount}
                  placeholder="0.00"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                />
                <Text style={styles.currencyText}>USDT</Text>
              </View>
            </View>

            {/* Rate Field */}
            <View style={styles.formGroup}>
              <Text style={styles.formLabel}>Enter rate (Fiat/USDT):</Text>
              <View style={styles.rateContainer}>
                <View style={styles.ratePrefix}>
                  <Text style={styles.ratePrefixText}>$1 = #</Text>
                </View>
                <TextInput
                  style={[styles.input, styles.rateInput]}
                  value={rate}
                  onChangeText={setRate}
                  placeholder="1500"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                />
              </View>
              <Text style={styles.rateExample}>Example: $1 = #1,500</Text>
            </View>

            {/* Limit Range Field */}
            <View style={styles.formGroup}>
              <Text style={styles.formLabel}>Enter limit range (Fiat):</Text>
              <View style={styles.inputContainer}>
                <TextInput
                  style={styles.input}
                  value={limitRange}
                  onChangeText={setLimitRange}
                  placeholder="0.00"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                />
                <Text style={styles.currencyText}>NGN</Text>
              </View>
              <Text style={styles.helperText}>
                Minimum amount buyers can transact
              </Text>
            </View>

            {/* Calculation Preview */}
            <View style={styles.previewCard}>
              <Text style={styles.previewTitle}>Transaction Preview</Text>

              <View style={styles.previewRow}>
                <Text style={styles.previewLabel}>You're selling:</Text>
                <Text style={styles.previewValue}>{amount || '0'} USDT</Text>
              </View>

              <View style={styles.previewRow}>
                <Text style={styles.previewLabel}>At rate:</Text>
                <Text style={styles.previewValue}>$1 = #{rate || '0'}</Text>
              </View>

              <View style={styles.previewRow}>
                <Text style={styles.previewLabel}>Total value:</Text>
                <Text style={styles.previewValue}>
                  #{(parseFloat(amount || '0') * parseFloat(rate || '0')).toLocaleString()}
                </Text>
              </View>

              <View style={styles.previewRow}>
                <Text style={styles.previewLabel}>Limit range:</Text>
                <Text style={styles.previewValue}>#{limitRange || '0'}</Text>
              </View>
            </View>

            {/* Important Notes */}
            <View style={styles.notesContainer}>
              <Text style={styles.notesTitle}>Important Notes:</Text>

              <View style={styles.noteItem}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.noteText}>
                  USDT will be locked from your account upon ad upload
                </Text>
              </View>

              <View style={styles.noteItem}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.noteText}>
                  Ad duration: 30 days (funds released if not bought)
                </Text>
              </View>

              <View style={styles.noteItem}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.noteText}>
                  Set competitive rates to attract more buyers
                </Text>
              </View>

              <View style={styles.noteItem}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.noteText}>
                  Include low fees in your rate calculation
                </Text>
              </View>

              <View style={styles.noteItem}>
                <Icon name="warning" size={16} color={GOLD_COLORS.error} />
                <Text style={[styles.noteText, styles.warningNote]}>
                  Funds in account must match limit range
                </Text>
              </View>
            </View>

            {/* Upload Button */}
            <TouchableOpacity
              style={styles.uploadButton}
              onPress={handleUpload}
              activeOpacity={0.8}
            >
              <Icon name="cloud-upload" size={24} color="#000000" />
              <Text style={styles.uploadButtonText}>Upload Ad</Text>
            </TouchableOpacity>
          </View>
        </View>
      </ScrollView>

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
    fontSize: 22,
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
    marginBottom: 12,
  },
  description: {
    fontSize: 16,
    color: '#666666',
    lineHeight: 24,
    marginBottom: 24,
  },
  formContainer: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  formGroup: {
    marginBottom: 24,
  },
  formLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 8,
  },
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 14,
  },
  input: {
    flex: 1,
    fontSize: 18,
    fontWeight: '600',
    color: '#000000',
  },
  currencyText: {
    fontSize: 16,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
    marginLeft: 12,
  },
  rateContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  ratePrefix: {
    backgroundColor: GOLD_COLORS.light,
    borderTopLeftRadius: 12,
    borderBottomLeftRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 14,
    justifyContent: 'center',
  },
  ratePrefixText: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
  },
  rateInput: {
    flex: 1,
    backgroundColor: GOLD_COLORS.light,
    borderTopRightRadius: 12,
    borderBottomRightRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 14,
    borderLeftWidth: 1,
    borderLeftColor: GOLD_COLORS.muted,
  },
  rateExample: {
    fontSize: 14,
    color: '#666666',
    marginTop: 8,
    fontStyle: 'italic',
  },
  helperText: {
    fontSize: 14,
    color: '#666666',
    marginTop: 8,
  },
  previewCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
  },
  previewTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 16,
  },
  previewRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  previewLabel: {
    fontSize: 14,
    color: '#666666',
  },
  previewValue: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
  },
  notesContainer: {
    backgroundColor: '#FFF8E1',
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
  },
  notesTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  noteItem: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 8,
  },
  noteText: {
    fontSize: 14,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    lineHeight: 20,
  },
  warningNote: {
    color: GOLD_COLORS.error,
  },
  uploadButton: {
    backgroundColor: GOLD_COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 12,
    paddingVertical: 18,
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  uploadButtonText: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginLeft: 12,
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
  confirmText: {
    fontSize: 16,
    color: '#666666',
    marginBottom: 20,
    textAlign: 'center',
  },
  detailsCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
  },
  detailsTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  detailLabel: {
    fontSize: 14,
    color: '#666666',
  },
  detailValue: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
  },
  warningBox: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#FFE5E5',
    padding: 16,
    borderRadius: 12,
  },
  warningText: {
    fontSize: 14,
    color: GOLD_COLORS.error,
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
  confirmButton: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 12,
    paddingVertical: 16,
  },
  confirmButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
});

export default P2PBuyScreen;