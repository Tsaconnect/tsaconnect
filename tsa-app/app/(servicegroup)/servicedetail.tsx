// app/(servicegroup)/servicedetail.tsx
//
// Service detail — restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives). The service fetch
// (getServiceContact status check + api.getProductById catalogue fetch) and
// the contact-fee action flow (prepareContactFee → sign → broadcast → confirm
// via submitContactFee, the sign/broadcast/confirm + verification path) are
// preserved EXACTLY — this is a visual/structural restyle only. The reveal
// flow's two-step approve + payContactFee broadcast, the withTimeout guards,
// the humanizePaymentError mapping, the USDC/USDT token selection, and the
// ServiceDetailCard composition (props unchanged) are all untouched.
import React, { useEffect, useState, useCallback } from 'react';
import { Modal, ScrollView, Image, Dimensions, FlatList, Alert, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import { useLocalSearchParams } from 'expo-router';
import ServiceDetailCard from '../../components/services/ServiceDetail';
import ServicePricingSheet from '../../components/services/ServicePricingSheet';
import { useAuth } from '../../AuthContext/AuthContext';
import { signTransaction, broadcastTransaction } from '../../services/wallet';
import {
  prepareContactFee,
  submitContactFee,
  getServiceContact,
  type ContactInfo,
} from '../../services/serviceContactApi';
import { api } from '../../components/services/api';
import { useTheme } from '@/theme';
import {
  Text,
  Button,
  IconButton,
  SegmentedControl,
  Sheet,
  Spinner,
} from '@/components/ui';
import { ListingDetails } from '@/components/forms/ListingDetails';
import { getServiceProfile } from '../../services/serviceListingApi';
import { contactServiceCharge, type ContactServiceCharge } from '@/utils/contactServiceCharge';

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

const TOKEN_SEGMENTS = [
  { key: 'USDC' as const, label: 'USDC' },
  { key: 'USDT' as const, label: 'USDT' },
];

function providerFeeNote(charge: ContactServiceCharge | null): string | undefined {
  if (!charge) return undefined;
  if (charge.accessFeeUsd > 0) {
    const callout = charge.calloutFeeUsd > 0 ? ` and a $${charge.calloutFeeUsd.toFixed(2)} call-out` : '';
    return `Plus the provider's own fee of $${charge.accessFeeUsd.toFixed(2)}${callout}, paid after you contact them.`;
  }
  return charge.payable ? undefined : 'No other provider fee — this unlocks their contact details.';
}

const ServiceDetail = () => {
  const { colors, spacing, radius } = useTheme();
  const { token, currentUser } = useAuth();
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
  // The listing's answers to its category's listing fields, and that category.
  const [listing, setListing] = useState<{ categoryId?: string; values?: Record<string, unknown> }>({});
  const [showCatalogue, setShowCatalogue] = useState(false);
  // Service pricing sheet (price + call-out / booking fare → escrow checkout).
  // Gated in ServiceDetailCard: contact-type still requires the contact fee
  // first; order/booking types unlock the pricing flow immediately.
  const [showPricing, setShowPricing] = useState(false);
  // The category's serviceType drives the buyer flow. Resolved from the product
  // below; missing/unknown defaults to 'contact' (the legacy behavior).
  const [serviceType, setServiceType] = useState<'contact' | 'order' | 'booking'>('contact');
  // serviceType is resolved asynchronously (see the product-fetch effect). Until
  // it's known we must not render the contact-fee block, or an order/booking
  // listing would briefly flash "Reveal Contact — $fee" before hiding it.
  const [serviceTypeResolved, setServiceTypeResolved] = useState(false);
  // What a contact provider charges on top of the contact fee. Null until known;
  // a provider who charges nothing has no checkout, so no Pay Now to dead-end on.
  const [providerCharge, setProviderCharge] = useState<ContactServiceCharge | null>(null);

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

  // Check if user already paid — only for contact services, and only once the
  // type is resolved. Order/booking have no contact fee, so we never hit the
  // pay-to-unlock (402) path for them.
  useEffect(() => {
    if (!serviceId || !token) return;
    if (!serviceTypeResolved || serviceType !== 'contact') return;
    checkContactStatus();
  }, [serviceId, token, serviceTypeResolved, serviceType, checkContactStatus]);

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
      if (Array.isArray(imgs)) {
        const urls = imgs
          .map((img: any) => (typeof img === 'string' ? img : img?.url))
          .filter((u: unknown): u is string => typeof u === 'string' && u.length > 0);
        setCatalogueImages(urls);
      }

      // Resolve the category's serviceType (contact/order/booking) so the
      // pricing flow branches correctly and the pricing button ungates for
      // order/booking (which don't require the contact fee). Prefer an embedded
      // category object; else look the category up by id. Default: 'contact'.
      const cat = res.data?.category as any;
      setListing({
        categoryId: typeof cat === 'string' ? cat : cat?.id,
        values: (res.data as any)?.listingValues,
      });
      const embedded =
        cat && typeof cat === 'object' && typeof cat.serviceType === 'string'
          ? (cat.serviceType as string)
          : undefined;
      let resolved: 'contact' | 'order' | 'booking' = 'contact';
      if (embedded === 'order' || embedded === 'booking' || embedded === 'contact') {
        resolved = embedded;
      } else {
        const catId = typeof cat === 'string' ? cat : cat?.id;
        if (catId) {
          const cats = await api.getCategories({});
          if (cancelled) return;
          if (cats.success && Array.isArray(cats.data)) {
            const match = cats.data.find((c) => c.id === catId || c._id === catId);
            const st = match?.serviceType;
            if (st === 'order' || st === 'booking' || st === 'contact') resolved = st;
          }
        }
      }
      if (!cancelled) setServiceType(resolved);
      // Not awaited: the reveal block must not wait on it. A failed fetch leaves
      // the charge unknown (no note, Pay Now kept) rather than claiming no fee.
      if (resolved === 'contact') {
        getServiceProfile(serviceId)
          .then((prof) => {
            if (!cancelled && prof.success) setProviderCharge(contactServiceCharge(res.data as any, prof.data));
          })
          .catch(() => {});
      }
    })()
      .catch(() => {
        // Network failure — keep the default 'contact' type (legacy behavior).
      })
      .finally(() => {
        // Mark resolved either way so the contact block can render for contact
        // services; on failure the default 'contact' preserves legacy behavior.
        if (!cancelled) setServiceTypeResolved(true);
      });
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
        // No `|| '14601'`. That silently signed for Sonic TESTNET whenever the
        // backend named no chain, in a flow that broadcasts to mainnet. Passed
        // through raw so signTransaction refuses it by name instead.
        chainId: approveTx.chainId,
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
        chainId: payFeeTx.chainId,
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        {/* Loading status */}
        {loading && paymentStep !== 'idle' && (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.sm,
              backgroundColor: colors.accentMuted,
              borderRadius: radius.md,
              paddingVertical: spacing.sm,
              paddingHorizontal: spacing.lg,
              marginBottom: spacing.lg,
            }}
          >
            <Spinner inline />
            <Text variant="bodySm" weight="600" color="accent">
              {STEP_LABELS[paymentStep]}
            </Text>
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
          onPricingPress={
            serviceType === 'contact' && providerCharge && !providerCharge.payable
              ? undefined
              : () => setShowPricing(true)
          }
          providerFeeNote={serviceType === 'contact' ? providerFeeNote(providerCharge) : undefined}
          serviceType={serviceType}
          serviceTypeResolved={serviceTypeResolved}
        />
        <ListingDetails
          categoryId={listing.categoryId}
          values={listing.values}
          style={{ marginTop: spacing.lg }}
        />
      </ScrollView>

      {/* Service pricing & escrow checkout sheet (gated by contactPaid above) */}
      {serviceId ? (
        <ServicePricingSheet
          visible={showPricing}
          onClose={() => setShowPricing(false)}
          serviceId={serviceId}
          buyerCity={currentUser?.city}
          buyerState={currentUser?.state}
          buyerCountry={currentUser?.country}
        />
      ) : null}

      {/* Payment Confirmation Sheet */}
      <Sheet visible={showModal} onClose={() => setShowModal(false)} title="Reveal Contact Details">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.lg }}>
          <Text variant="bodySm" color="textSecondary">
            Pay {feeDisplay} in {selectedToken} to reveal this provider&apos;s contact details.
          </Text>

          {/* Token selector — contract uses a single global feeAmount sized
              for 6-decimal stablecoins, so only USDC/USDT are supported. */}
          <SegmentedControl
            segments={TOKEN_SEGMENTS}
            value={selectedToken}
            onChange={setSelectedToken}
          />

          <Button title="Pay & Reveal" onPress={handlePayAndReveal} size="lg" />
          <Button title="Cancel" variant="ghost" onPress={() => setShowModal(false)} />
        </View>
      </Sheet>

      {/* Catalogue gallery — shows the merchant's full multi-image upload.
          Kept as an immersive full-screen lightbox over the scrim token so the
          merchant's photos read edge-to-edge regardless of light/dark scheme. */}
      <Modal
        visible={showCatalogue}
        animationType="fade"
        transparent
        onRequestClose={() => setShowCatalogue(false)}
      >
        <View style={{ flex: 1, backgroundColor: colors.overlay, justifyContent: 'center' }}>
          <View
            style={{
              position: 'absolute',
              top: spacing['4xl'],
              left: 0,
              right: 0,
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'center',
              paddingHorizontal: spacing.xl,
              zIndex: 1,
            }}
          >
            <Text variant="title3" weight="700" style={{ color: colors.textInverse }}>
              Catalogue
            </Text>
            <IconButton
              icon="close"
              accessibilityLabel="Close catalogue"
              onPress={() => setShowCatalogue(false)}
              size={24}
              color={colors.textInverse}
            />
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
                style={{ width: Dimensions.get('window').width, height: '100%' }}
                resizeMode="contain"
              />
            )}
          />
          <Text
            variant="bodySm"
            align="center"
            style={{
              position: 'absolute',
              bottom: spacing['4xl'],
              left: 0,
              right: 0,
              color: colors.textInverse,
            }}
          >
            {catalogueImages.length} image{catalogueImages.length === 1 ? '' : 's'} — swipe to browse
          </Text>
        </View>
      </Modal>
    </SafeAreaView>
  );
};

export default ServiceDetail;
