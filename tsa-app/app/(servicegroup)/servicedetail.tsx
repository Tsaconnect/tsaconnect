import {
  StyleSheet,
  Text,
  View,
  Modal,
  TouchableOpacity,
  Alert,
  ScrollView,
  Image,
  Dimensions,
  FlatList,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
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
import { api } from '../../components/services/api';

type PaymentStep = 'idle' | 'approving' | 'paying' | 'revealing' | 'done';

// ApiError is thrown when the backend returned a structured error response.
// Its message is already user-friendly (the API author wrote it for users),
// so the alert path passes it through verbatim instead of pattern-matching.
class ApiError extends Error {
  readonly fromApi = true;
}

// humanizePaymentError maps the noisy ethers/network/contract errors that
// surface during the contact-fee flow into plain-language alerts. The raw
// strings (e.g. `transaction execution reverted (action="sendTransaction",
// data=null, reason=null, …)`) are useless to the user, so we look for
// well-known substrings and pick a sensible message. Errors flagged as
// `fromApi` skip this entirely and use the BE's own message.
function humanizePaymentError(err: unknown, tokenSymbol: string): string {
  if ((err as any)?.fromApi && (err as Error)?.message) {
    return (err as Error).message;
  }
  const raw = String((err as any)?.message ?? err ?? '').toLowerCase();
  const has = (needle: string) => raw.includes(needle.toLowerCase());

  // Network / RPC failures
  if (has('network request failed') || has('failed to detect network')) {
    return 'Network issue while reaching the blockchain. Check your connection and try again.';
  }
  if (has('timed out')) return (err as Error).message; // already user-friendly from withTimeout

  // ERC-20 + ServiceContact custom reverts
  if (has('exceeds balance') || has('transfer amount exceeds balance')) {
    return `You don't have enough ${tokenSymbol} on Sonic to pay the contact fee. Please fund your wallet and try again.`;
  }
  if (has('exceeds allowance') || has('insufficient allowance')) {
    return 'The token approval did not go through. Please try again.';
  }
  if (has('cannot pay for own contact')) return "You can't pay the contact fee for your own service.";
  if (has('token not accepted')) return `${tokenSymbol} isn't accepted for contact fees right now.`;
  if (has('fee not configured for token')) return `${tokenSymbol} fee isn't configured yet — contact support.`;
  if (has('enforcedpause') || has('paused')) return 'Service contact payments are temporarily paused. Try again later.';
  if (has('cannot pay for own')) return "You can't pay the contact fee for your own service.";

  // Generic transaction-execution-reverted with no extra detail (typical
  // when the contract rejected the call without a reason string).
  if (has('transaction execution reverted') || has('execution reverted')) {
    return 'The on-chain payment was rejected. The most common cause is insufficient ' +
      `${tokenSymbol} balance — please verify your wallet has enough on Sonic.`;
  }

  // User cancelled in their wallet
  if (has('user rejected') || has('user denied')) return 'You cancelled the transaction.';

  // Receipt status === 0 path from the catch
  if (has('failed or was dropped')) return 'The transaction was dropped or reverted on-chain. Please try again.';

  // Verification failure from the BE (after broadcast)
  if (has('contactfeepaid event not found')) {
    return 'The payment went through on-chain but the system could not verify it. Please contact support with your transaction hash.';
  }

  // Anything else — fall back to a short prefix; never dump the raw JSON.
  return 'Payment failed. Please try again, or contact support if it keeps happening.';
}

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
  // Catalogue images come from the merchant's multi-image upload on
  // AddService. The detail card only shows the hero; "See Catalogue" opens
  // a modal with the rest of the gallery.
  const [catalogueImages, setCatalogueImages] = useState<string[]>([]);
  const [showCatalogue, setShowCatalogue] = useState(false);

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

  // Fetch the full product so we can render the catalogue gallery — the
  // navigation params only carry the hero image. Network failure leaves
  // the catalogue button hidden, which is the right fallback.
  useEffect(() => {
    if (!serviceId) return;
    let cancelled = false;
    (async () => {
      const res = await api.getProductById(serviceId);
      if (cancelled) return;
      const imgs = res.data?.images;
      if (!Array.isArray(imgs)) return;
      const urls = imgs
        .map((img: any) => (typeof img === 'string' ? img : img?.url))
        .filter((u: unknown): u is string => typeof u === 'string' && u.length > 0);
      setCatalogueImages(urls);
    })();
    return () => {
      cancelled = true;
    };
  }, [serviceId]);

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
        throw new ApiError(prepResult.message || 'Failed to prepare transactions');
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
        throw new ApiError(submitResult.message || 'Payment verification failed');
      }

      setContact(submitResult.data.contact);
      setContactPaid(true);
      setPaymentStep('done');
    } catch (error: any) {
      console.warn('[contact-fee]', error);
      Alert.alert('Payment Failed', humanizePaymentError(error, selectedToken));
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
        onCataloguePress={catalogueImages.length > 0 ? () => setShowCatalogue(true) : undefined}
      />

      {/* Payment Confirmation Modal */}
      <Modal visible={showModal} transparent animationType="slide">
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>Reveal Contact Details</Text>
            <Text style={styles.modalDesc}>
              Pay {feeDisplay} in {selectedToken} to reveal this provider's contact details.
            </Text>

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

      {/* Catalogue gallery — shows the merchant's full multi-image upload. */}
      <Modal
        visible={showCatalogue}
        animationType="fade"
        transparent
        onRequestClose={() => setShowCatalogue(false)}
      >
        <View style={styles.catalogueOverlay}>
          <View style={styles.catalogueHeader}>
            <Text style={styles.catalogueTitle}>Catalogue</Text>
            <TouchableOpacity
              onPress={() => setShowCatalogue(false)}
              hitSlop={12}
            >
              <Ionicons name="close" size={26} color="#fff" />
            </TouchableOpacity>
          </View>
          <FlatList
            data={catalogueImages}
            horizontal
            pagingEnabled
            showsHorizontalScrollIndicator={false}
            keyExtractor={(uri, idx) => `${idx}-${uri}`}
            renderItem={({ item }) => (
              <Image
                source={{ uri: item }}
                style={styles.catalogueImage}
                resizeMode="contain"
              />
            )}
          />
          <Text style={styles.catalogueHint}>
            {catalogueImages.length} image{catalogueImages.length === 1 ? '' : 's'} — swipe to browse
          </Text>
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

  // Catalogue gallery modal
  catalogueOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.92)',
    justifyContent: 'center',
  },
  catalogueHeader: {
    position: 'absolute',
    top: 40,
    left: 0,
    right: 0,
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    zIndex: 1,
  },
  catalogueTitle: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '700',
  },
  catalogueImage: {
    width: Dimensions.get('window').width,
    height: '100%',
  },
  catalogueHint: {
    position: 'absolute',
    bottom: 40,
    left: 0,
    right: 0,
    textAlign: 'center',
    color: 'rgba(255,255,255,0.7)',
    fontSize: 13,
  },
});
