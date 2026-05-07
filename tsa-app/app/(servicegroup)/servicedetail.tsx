import {
  StyleSheet,
  Text,
  View,
  Modal,
  TouchableOpacity,
  Alert,
  ScrollView,
} from 'react-native';
import React, { useEffect, useState, useCallback } from 'react';
import * as Clipboard from 'expo-clipboard';
import ServiceDetailCard from '../../components/services/ServiceDetail';
import { useLocalSearchParams } from 'expo-router';
import { useAuth } from '../../AuthContext/AuthContext';
import { COLORS } from '../../constants';
import { signTransaction, broadcastTransaction } from '../../services/wallet';
import {
  prepareContactFee,
  submitContactFee,
  getServiceContact,
  type ContactInfo,
} from '../../services/serviceContactApi';

type PaymentStep = 'idle' | 'approving' | 'paying' | 'revealing' | 'done';

// withTimeout wraps a promise so a broadcast/wait that hangs (flaky RPC,
// network drop, dead node) eventually surfaces a real error instead of
// silently spinning forever.
function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error(`${label} timed out after ${Math.round(ms / 1000)}s — check your network and try again`)),
      ms,
    );
  });
  return Promise.race([promise, timeout]).finally(() => {
    if (timer) clearTimeout(timer);
  }) as Promise<T>;
}

const STEP_LABELS: Record<PaymentStep, string> = {
  idle: '',
  approving: 'Approving token...',
  paying: 'Processing payment...',
  revealing: 'Revealing contact...',
  done: '',
};

const ServiceDetail = () => {
  const { token } = useAuth();
  const { title, image, description, id: serviceId } = useLocalSearchParams<{
    title: string;
    image: string;
    description: string;
    id: string;
  }>();

  const [contactPaid, setContactPaid] = useState(false);
  const [contact, setContact] = useState<ContactInfo | null>(null);
  const [feeDisplay, setFeeDisplay] = useState('$0.10');
  const [loading, setLoading] = useState(false);
  const [paymentStep, setPaymentStep] = useState<PaymentStep>('idle');
  const [showModal, setShowModal] = useState(false);
  const [selectedToken, setSelectedToken] = useState<'USDC' | 'USDT'>('USDC');

  const checkContactStatus = useCallback(async () => {
    if (!serviceId) return;
    try {
      const result = await getServiceContact(serviceId);
      if (result.data?.paid && result.data?.contact) {
        setContactPaid(true);
        setContact(result.data.contact);
      } else if (result.data?.feeAmount) {
        // Derive fee display from backend response
        const cents = parseInt(result.data.feeAmount, 10);
        if (!isNaN(cents)) {
          setFeeDisplay(`$${(cents / 1_000_000).toFixed(2)}`);
        }
      }
    } catch {
      // Not paid or error — show locked state
    }
  }, [serviceId]);

  // Check if user already paid on mount
  useEffect(() => {
    if (!serviceId || !token) return;
    checkContactStatus();
  }, [serviceId, token, checkContactStatus]);

  const handleRevealPress = () => {
    setShowModal(true);
  };

  const handlePayAndReveal = async () => {
    if (!serviceId) return;
    setShowModal(false);
    setLoading(true);

    try {
      // Step 1: Prepare transactions
      setPaymentStep('approving');
      const prepResult = await prepareContactFee(serviceId, selectedToken);
      if (!prepResult.success || !prepResult.data) {
        throw new Error(prepResult.message || 'Failed to prepare transactions');
      }

      const { approveTx, payFeeTx } = prepResult.data;

      // Step 2: Sign and broadcast approve tx
      const approveUnsigned = {
        to: approveTx.to,
        data: '0x' + approveTx.data,
        value: BigInt(approveTx.value || '0'),
        gasLimit: BigInt(approveTx.gasLimit || 100000),
        gasPrice: BigInt(approveTx.gasPrice || '0'),
        nonce: approveTx.nonce,
        chainId: BigInt(approveTx.chainId || '14601'),
        type: 0,
      };

      const signedApprove = await signTransaction(approveUnsigned);
      const approveResponse = await withTimeout(
        broadcastTransaction('sonic', signedApprove),
        30_000,
        'Approve broadcast',
      );
      const approveReceipt = await withTimeout(approveResponse.wait(), 60_000, 'Approve confirmation');

      // Fix #7: Check receipt is non-null and successful
      if (!approveReceipt || approveReceipt.status === 0) {
        throw new Error('Approve transaction failed or was dropped');
      }

      // Step 3: Sign and broadcast payContactFee tx
      setPaymentStep('paying');
      const payFeeUnsigned = {
        to: payFeeTx.to,
        data: '0x' + payFeeTx.data,
        value: BigInt(payFeeTx.value || '0'),
        gasLimit: BigInt(payFeeTx.gasLimit || 200000),
        gasPrice: BigInt(payFeeTx.gasPrice || '0'),
        nonce: approveTx.nonce + 1, // nonce after approve
        chainId: BigInt(payFeeTx.chainId || '14601'),
        type: 0,
      };

      const signedPayFee = await signTransaction(payFeeUnsigned);
      const payFeeResponse = await withTimeout(
        broadcastTransaction('sonic', signedPayFee),
        30_000,
        'Pay fee broadcast',
      );
      const payFeeReceipt = await withTimeout(payFeeResponse.wait(), 60_000, 'Pay fee confirmation');

      // Fix #7: Check receipt is non-null and successful
      if (!payFeeReceipt || payFeeReceipt.status === 0) {
        throw new Error('Pay fee transaction failed or was dropped');
      }

      // Step 4: Submit to backend for verification
      setPaymentStep('revealing');
      const submitResult = await submitContactFee(
        serviceId,
        approveResponse.hash,
        payFeeResponse.hash,
        selectedToken
      );

      if (!submitResult.success || !submitResult.data) {
        throw new Error(submitResult.message || 'Payment verification failed');
      }

      setContact(submitResult.data.contact);
      setContactPaid(true);
      setPaymentStep('done');
    } catch (error: any) {
      Alert.alert('Payment Failed', error.message || 'Something went wrong');
      setPaymentStep('idle');
    } finally {
      setLoading(false);
    }
  };

  const handleCopy = async (value: string) => {
    await Clipboard.setStringAsync(value);
    Alert.alert('Copied', 'Copied to clipboard');
  };

  return (
    <ScrollView style={styles.container}>
      {/* Loading status */}
      {loading && paymentStep !== 'idle' && (
        <View style={styles.statusBar}>
          <Text style={styles.statusText}>{STEP_LABELS[paymentStep]}</Text>
        </View>
      )}

      <ServiceDetailCard
        title={title as string}
        imageSrc={image as string}
        address={description as string}
        contactPaid={contactPaid}
        contact={contact}
        feeDisplay={feeDisplay}
        loading={loading}
        onRevealPress={handleRevealPress}
        onCopyPress={handleCopy}
      />

      {/* Payment Confirmation Modal */}
      <Modal visible={showModal} transparent animationType="slide">
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>Reveal Contact Details</Text>
            <Text style={styles.modalDesc}>
              Pay {feeDisplay} in {selectedToken} to reveal this provider's contact details.
            </Text>

            <View style={styles.splitInfo}>
              <Text style={styles.splitTitle}>Fee breakdown:</Text>
              <Text style={styles.splitLine}>Provider: 25%</Text>
              <Text style={styles.splitLine}>You (cashback): 12.5%</Text>
              <Text style={styles.splitLine}>Referral upline: 12.5%</Text>
              <Text style={styles.splitLine}>Platform: 50%</Text>
            </View>

            {/* Token selector — contract uses a single global feeAmount sized
                for 6-decimal stablecoins, so only USDC/USDT are supported. */}
            <View style={styles.tokenSelector}>
              {(['USDC', 'USDT'] as const).map((tk) => (
                <TouchableOpacity
                  key={tk}
                  style={[
                    styles.tokenPill,
                    selectedToken === tk && styles.tokenPillActive,
                  ]}
                  onPress={() => setSelectedToken(tk)}
                >
                  <Text
                    style={[
                      styles.tokenPillText,
                      selectedToken === tk && styles.tokenPillTextActive,
                    ]}
                  >
                    {tk}
                  </Text>
                </TouchableOpacity>
              ))}
            </View>

            <TouchableOpacity style={styles.payButton} onPress={handlePayAndReveal}>
              <Text style={styles.payButtonText}>Pay & Reveal</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.cancelButton}
              onPress={() => setShowModal(false)}
            >
              <Text style={styles.cancelButtonText}>Cancel</Text>
            </TouchableOpacity>
          </View>
        </View>
      </Modal>
    </ScrollView>
  );
};

export default ServiceDetail;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#f9fafb',
  },
  statusBar: {
    backgroundColor: COLORS.primary,
    paddingVertical: 10,
    paddingHorizontal: 16,
    alignItems: 'center',
  },
  statusText: {
    color: '#fff',
    fontSize: 14,
    fontWeight: '600',
  },
  // Modal
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'flex-end',
  },
  modalContent: {
    backgroundColor: '#fff',
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    padding: 24,
    paddingBottom: 40,
  },
  modalTitle: {
    fontSize: 20,
    fontWeight: 'bold',
    color: COLORS.primary,
    marginBottom: 8,
  },
  modalDesc: {
    fontSize: 14,
    color: '#555',
    marginBottom: 16,
    lineHeight: 20,
  },
  splitInfo: {
    backgroundColor: '#f9f6f2',
    borderRadius: 10,
    padding: 12,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: '#E8D5C0',
  },
  splitTitle: {
    fontSize: 13,
    fontWeight: '600',
    color: COLORS.primary,
    marginBottom: 6,
  },
  splitLine: {
    fontSize: 13,
    color: '#555',
    marginBottom: 2,
  },
  tokenSelector: {
    flexDirection: 'row',
    gap: 10,
    marginBottom: 20,
  },
  tokenPill: {
    flex: 1,
    paddingVertical: 10,
    borderRadius: 20,
    borderWidth: 1,
    borderColor: '#E8D5C0',
    alignItems: 'center',
  },
  tokenPillActive: {
    backgroundColor: COLORS.primary,
    borderColor: COLORS.primary,
  },
  tokenPillText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#777',
  },
  tokenPillTextActive: {
    color: '#fff',
  },
  payButton: {
    backgroundColor: '#E8A14A',
    paddingVertical: 14,
    borderRadius: 10,
    alignItems: 'center',
    marginBottom: 10,
  },
  payButtonText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: 'bold',
  },
  cancelButton: {
    paddingVertical: 10,
    alignItems: 'center',
  },
  cancelButtonText: {
    fontSize: 14,
    color: '#777',
  },
});
