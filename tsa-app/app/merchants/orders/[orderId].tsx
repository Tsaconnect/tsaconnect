// app/merchants/orders/[orderId].tsx
//
// Merchant order detail — status, buyer, shipping, product, payment breakdown,
// lifecycle timeline, blockchain refs, and the ship / deliver / refund-decision /
// cancel-decision / dispute action flows. Restyled onto the TSA Signature design
// system (@/theme tokens + @/components/ui primitives).
//
// MERCHANT / MONEY screen: every API call (getOrderById, markOrderShipped,
// markOrderDelivered, approveRefund/rejectRefund, approveCancelOrder/
// submitCancelOrder/rejectCancelOrder, raiseDispute, api.uploadImage), the
// sign+broadcast cancel flow (signAndBroadcast(prep.data.cancelTx, 'sonic')),
// all state, effects, validation, status/role gating, navigation, and the
// proof-image picker logic are preserved verbatim — this is a visual/structural
// restyle only. The legacy STATUS_COLORS hex palette is replaced by semantic
// theme tones; status strings/keys are unchanged.
import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  ScrollView,
  Alert,
  Linking,
  Image,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import * as ImagePicker from 'expo-image-picker';
import {
  getOrderById,
  markOrderShipped,
  markOrderDelivered,
  prepareDeliver,
  isSpendableOrder,
  approveRefund,
  rejectRefund,
  approveCancelOrder,
  submitCancelOrder,
  rejectCancelOrder,
  raiseDispute,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import { payoutStageText, sellerNet, shippingWithoutPayoutFee } from '@/utils/payoutFee';
import { signAndBroadcast } from '@/services/transaction';
import { MARKETPLACE_NETWORK } from '@/services/serviceCheckout';
import api from '@/components/services/api';
import { formatStatus, formatDate } from '@/constants/orderStatus';
import { merchantOrderTimeline } from '@/utils/merchantOrderTimeline';
import { useTheme } from '@/theme';
import { useCurrency } from '@/contexts/CurrencyContext';
import {
  Avatar,
  Badge,
  Button,
  Card,
  Divider,
  EmptyState,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  Stepper,
  Text,
} from '@/components/ui';
import type { StatusTone } from '@/components/ui';

// Sonic explorer for txs (the wallet currently only supports Sonic for these flows)
const TX_EXPLORER_URL = 'https://sonicscan.org/tx/';

// Map each marketplace order status onto a semantic design-system tone. Replaces
// the legacy STATUS_COLORS hex palette; the status keys are unchanged.
const STATUS_TONE: Record<string, StatusTone> = {
  pending_payment: 'warning',
  escrowed: 'info',
  shipped: 'info',
  delivered: 'success',
  completed: 'success',
  refund_requested: 'danger',
  cancel_requested: 'warning',
  refunded: 'neutral',
  cancelled: 'neutral',
};

function isValidHttpUrl(value: string): boolean {
  try {
    const u = new URL(value);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch {
    return false;
  }
}

// Resolve a section overline label/value-row in design-system style.
const CARD_LABEL = (text: string) => (
  <Text variant="overline" color="textMuted" style={{ marginBottom: 0 }}>
    {text}
  </Text>
);

const MerchantOrderDetail = () => {
  const { colors, spacing, radius } = useTheme();
  const { formatPrice } = useCurrency();
  const { orderId } = useLocalSearchParams<{ orderId: string }>();
  const [order, setOrder] = useState<Order | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [orderFetchFailed, setOrderFetchFailed] = useState(false);
  const [actionLoading, setActionLoading] = useState(false);
  const [recordedOnChain, setRecordedOnChain] = useState(false);

  // Ship modal
  const [shipModalVisible, setShipModalVisible] = useState(false);
  const [trackingNumber, setTrackingNumber] = useState('');
  const [shipNotes, setShipNotes] = useState('');

  // Deliver modal — proof image
  const [deliverModalVisible, setDeliverModalVisible] = useState(false);
  const [proofImageUri, setProofImageUri] = useState<string | null>(null);
  const [uploadingProof, setUploadingProof] = useState(false);

  // Refund decision modal
  const [refundModalVisible, setRefundModalVisible] = useState<'approve' | 'reject' | null>(null);
  const [refundNotes, setRefundNotes] = useState('');

  // Cancel decision modal
  const [cancelModalVisible, setCancelModalVisible] = useState<'approve' | 'reject' | null>(null);
  const [cancelNotes, setCancelNotes] = useState('');

  // Dispute modal
  const [disputeModalVisible, setDisputeModalVisible] = useState(false);
  const [disputeReason, setDisputeReason] = useState('');

  const fetchOrder = useCallback(async () => {
    if (!orderId) return;
    try {
      setError(null);
      const result = await getOrderById(orderId as string);
      if (result.success && result.data) {
        setOrder(result.data);
        setOrderFetchFailed(false);
      } else {
        setError(result.message || 'Failed to load order');
        setOrderFetchFailed(true);
      }
    } catch (err: any) {
      setError(err.message || 'Failed to load order');
      setOrderFetchFailed(true);
    }
  }, [orderId]);

  useEffect(() => {
    setLoading(true);
    fetchOrder().finally(() => setLoading(false));
  }, [fetchOrder]);

  const handleShip = async () => {
    if (!order) return;
    setActionLoading(true);
    const result = await markOrderShipped(order.id, {
      trackingNumber: trackingNumber.trim() || undefined,
      notes: shipNotes.trim() || undefined,
    });
    setActionLoading(false);
    if (result.success) {
      setShipModalVisible(false);
      setTrackingNumber('');
      setShipNotes('');
      Alert.alert('Shipped', 'Order marked as shipped. The buyer has been notified.');
      fetchOrder();
    } else {
      Alert.alert('Error', result.message || 'Failed to mark as shipped');
    }
  };

  const pickProofImage = async () => {
    const perm = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      Alert.alert('Permission needed', 'Please grant access to your photos to upload a delivery proof.');
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      quality: 0.7,
      allowsEditing: false,
    });
    if (!result.canceled && result.assets[0]) {
      setProofImageUri(result.assets[0].uri);
    }
  };

  const captureProofPhoto = async () => {
    const perm = await ImagePicker.requestCameraPermissionsAsync();
    if (!perm.granted) {
      Alert.alert('Permission needed', 'Please grant camera access to take a delivery proof photo.');
      return;
    }
    const result = await ImagePicker.launchCameraAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      quality: 0.7,
      allowsEditing: false,
    });
    if (!result.canceled && result.assets[0]) {
      setProofImageUri(result.assets[0].uri);
    }
  };

  const handleDeliver = async () => {
    if (!order) return;
    if (!proofImageUri) {
      Alert.alert('Proof required', 'Please attach a photo of the delivery (signed receipt, package handed over, etc.)');
      return;
    }

    setActionLoading(true);
    setUploadingProof(true);
    try {
      const upload = await api.uploadImage(proofImageUri, 'document');
      setUploadingProof(false);
      if (!upload.success || !upload.data?.url) {
        setActionLoading(false);
        Alert.alert('Upload failed', upload.message || 'Could not upload the delivery proof. Please try again.');
        return;
      }
      const proofUrl = upload.data.url;
      // Defensive client-side validation in case the upload service returns a malformed URL
      if (!isValidHttpUrl(proofUrl)) {
        setActionLoading(false);
        Alert.alert('Invalid proof URL', 'The uploaded proof returned an invalid URL. Please try again.');
        return;
      }

      const result = await markOrderDelivered(order.id, proofUrl, await signDelivery(order));
      setActionLoading(false);
      if (result.success) {
        setDeliverModalVisible(false);
        setProofImageUri(null);
        Alert.alert('Delivered', 'Order marked as delivered. The buyer can now confirm receipt.');
        fetchOrder();
      } else {
        Alert.alert('Error', result.message || 'Failed to mark as delivered');
      }
    } catch (err: any) {
      setUploadingProof(false);
      setActionLoading(false);
      Alert.alert('Error', err.message || 'Failed to mark as delivered');
    }
  };

  // Escrow orders: the seller signs markDelivered, otherwise the buyer's refund
  // stays instant and full. Spendable MCGP orders have no escrow tx.
  const signDelivery = async (target: Order): Promise<string | undefined> => {
    if (isSpendableOrder(target)) return undefined;
    const prep = await prepareDeliver(target.id);
    // An earlier attempt already put it on-chain; the server accepts that as proof.
    // (or the server signs it for this order: its seller on-chain is the treasury).
    if (prep.code === 'delivery_on_chain' || prep.code === 'delivery_server_signed') return undefined;
    if (!prep.success || !prep.data?.deliverTx) {
      throw new Error(prep.message || 'Failed to prepare delivery');
    }
    return signAndBroadcast(prep.data.deliverTx, 'sonic', MARKETPLACE_NETWORK);
  };

  // Service orders have no physical delivery proof — the backend makes the
  // proof optional for services. Mark rendered with no proof, no image modal.
  const handleMarkRendered = async () => {
    if (!order) return;
    setActionLoading(true);
    let result;
    try {
      result = await markOrderDelivered(order.id, '', await signDelivery(order));
    } catch (err: any) {
      result = { success: false, message: err.message };
    }
    setActionLoading(false);
    if (result.success) {
      Alert.alert('Service Rendered', 'Marked as rendered. The buyer can now confirm completion.');
      fetchOrder();
    } else {
      Alert.alert('Error', result.message || 'Failed to mark service rendered');
    }
  };

  // Orders delivered before delivery went on-chain: sign it now, so a buyer's
  // refund request becomes a dispute instead of an instant refund.
  const handleRecordOnChain = async () => {
    if (!order) return;
    setActionLoading(true);
    let result;
    try {
      result = await markOrderDelivered(order.id, order.deliveryProofUrl || '', await signDelivery(order));
    } catch (err: any) {
      result = { success: false, message: err.message };
    }
    setActionLoading(false);
    if (result.success) {
      setRecordedOnChain(true);
      Alert.alert('Recorded', 'Delivery is now recorded on-chain.');
    } else {
      Alert.alert('Error', result.message || 'Failed to record delivery');
    }
  };

  const handleRefundDecision = async () => {
    if (!order || !refundModalVisible) return;
    setActionLoading(true);
    const fn = refundModalVisible === 'approve' ? approveRefund : rejectRefund;
    const result = await fn(order.id, refundNotes.trim() || undefined);
    setActionLoading(false);
    if (result.success) {
      const action = refundModalVisible === 'approve' ? 'approved' : 'rejected';
      setRefundModalVisible(null);
      setRefundNotes('');
      Alert.alert(
        `Refund ${action}`,
        refundModalVisible === 'approve'
          ? "You've approved the refund. An admin will finalize it on-chain shortly."
          : 'The refund has been rejected and the order returned to delivered status.'
      );
      fetchOrder();
    } else {
      Alert.alert('Error', result.message || `Failed to ${refundModalVisible} refund`);
    }
  };

  const handleCancelDecision = async () => {
    if (!order || !cancelModalVisible) return;

    if (cancelModalVisible === 'reject') {
      setActionLoading(true);
      const result = await rejectCancelOrder(order.id, cancelNotes.trim() || undefined);
      setActionLoading(false);
      if (result.success) {
        setCancelModalVisible(null);
        setCancelNotes('');
        Alert.alert('Cancel Rejected', 'The order returns to escrowed. The buyer has been notified.');
        fetchOrder();
      } else {
        Alert.alert('Error', result.message || 'Failed to reject cancel request');
      }
      return;
    }

    // Approve — prepare, sign, submit
    setActionLoading(true);
    try {
      const prep = await approveCancelOrder(order.id);
      if (!prep.success) {
        throw new Error(prep.message || 'Failed to prepare cancel transaction');
      }
      // A Spendable MCGP order has no escrow tx: approving already refunded it.
      if (!prep.data?.cancelled) {
        if (!prep.data?.cancelTx) throw new Error('Failed to prepare cancel transaction');
        const txHash = await signAndBroadcast(prep.data.cancelTx, 'sonic', MARKETPLACE_NETWORK);
        const submit = await submitCancelOrder(order.id, txHash);
        if (!submit.success) {
          throw new Error(submit.message || 'Failed to submit cancel');
        }
      }
      setCancelModalVisible(null);
      setCancelNotes('');
      Alert.alert('Cancel Approved', 'Funds have been returned to the buyer.');
      fetchOrder();
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to approve cancel');
    } finally {
      setActionLoading(false);
    }
  };

  const submitDispute = async () => {
    if (!order) return;
    const reason = disputeReason.trim();
    if (reason.length < 10) {
      Alert.alert('More detail needed', 'Please describe the issue in at least 10 characters.');
      return;
    }
    setActionLoading(true);
    setDisputeModalVisible(false);
    try {
      const result = await raiseDispute(order.id, reason);
      if (!result.success) throw new Error(result.message || 'Failed to raise dispute');
      Alert.alert('Dispute Submitted', 'Admin will review and resolve shortly.');
      setDisputeReason('');
      fetchOrder();
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to raise dispute');
    } finally {
      setActionLoading(false);
    }
  };

  const openExplorer = async (hash: string) => {
    const url = TX_EXPLORER_URL + hash;
    try {
      const supported = await Linking.canOpenURL(url);
      if (!supported) {
        Alert.alert('Could not open link', 'No app available to open the blockchain explorer.');
        return;
      }
      await Linking.openURL(url);
    } catch (err: any) {
      Alert.alert('Could not open link', err.message || 'Failed to open the blockchain explorer.');
    }
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading order…" />
        </View>
      </SafeAreaView>
    );
  }

  if (orderFetchFailed || !order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', padding: spacing.lg }}>
          <EmptyState
            icon="warning"
            title={error || 'Order not found'}
            subtitle="We couldn't load this order."
            actionLabel="Retry"
            onAction={() => { setLoading(true); fetchOrder().finally(() => setLoading(false)); }}
          />
          <Button
            title="Go back"
            variant="ghost"
            onPress={() => router.back()}
            style={{ marginTop: spacing.md }}
          />
        </View>
      </SafeAreaView>
    );
  }

  const tone = STATUS_TONE[order.status] ?? 'neutral';
  const toneColorMap: Record<StatusTone, { fg: string; bg: string }> = {
    success: { fg: colors.success, bg: colors.successBg },
    warning: { fg: colors.warning, bg: colors.warningBg },
    danger: { fg: colors.danger, bg: colors.dangerBg },
    info: { fg: colors.info, bg: colors.infoBg },
    escrow: { fg: colors.escrow, bg: colors.escrowBg },
    accent: { fg: colors.accent, bg: colors.accentMuted },
    neutral: { fg: colors.textSecondary, bg: colors.surfaceAlt },
  };
  const statusVisual = toneColorMap[tone];

  const isDisputed = !!order.disputedAt;
  // Service orders skip the shipping leg: no "Mark shipped"; the provider taps
  // "Mark service rendered" (reuses markOrderDelivered, proof optional) straight
  // from escrowed; "Delivered" reads as "Rendered" (spec §6).
  const isService = order.kind === 'service';
  const canShip = !isDisputed && !isService && order.status === 'escrowed';
  const canDeliver =
    !isDisputed && (isService ? order.status === 'escrowed' : order.status === 'shipped');
  const canRecordOnChain =
    !isDisputed && !recordedOnChain && !isSpendableOrder(order) && order.status === 'delivered';
  const canResolveRefund = !isDisputed && order.status === 'refund_requested';
  const canHandleCancel = !isDisputed && order.status === 'cancel_requested';
  const canRaiseDispute = !isDisputed && ['escrowed', 'shipped', 'delivered', 'cancel_requested', 'refund_requested'].includes(order.status);
  const timeline = merchantOrderTimeline(order, (iso) => formatDate(iso, true));
  const buyerLabel =
    order.buyer?.name?.trim() ||
    (order.buyer?.username ? `@${order.buyer.username}` : '') ||
    order.buyer?.email ||
    'Unknown buyer';
  const shippingAddressParts = [order.shippingStreet, order.shippingCity, order.shippingState, order.shippingCountry].filter(Boolean);

  const hasActionBar =
    !isDisputed && (canShip || canDeliver || canResolveRefund || canHandleCancel || canRaiseDispute);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{
          padding: spacing.lg,
          paddingBottom: (hasActionBar || isDisputed) ? spacing['4xl'] * 3 : spacing['4xl'],
        }}
      >
        {/* Status card */}
        <Card style={{ marginBottom: spacing.md, alignItems: 'center' }}>
          <Badge
            label={isService && order.status === 'delivered' ? 'Rendered' : formatStatus(order.status)}
            color={statusVisual.fg}
            background={statusVisual.bg}
          />
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm }}>
            Order #{order.id.slice(0, 8)}
          </Text>
          {order.merchantApprovedRefund && (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                backgroundColor: colors.successBg,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.xs,
                borderRadius: radius.sm,
                marginTop: spacing.md,
              }}
            >
              <Icon name="checkCircle" size={14} color={colors.success} />
              <Text variant="caption" weight="600" color="success">
                You approved this refund. Awaiting admin.
              </Text>
            </View>
          )}
          {order.status === 'cancel_requested' && order.cancelReason && (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                backgroundColor: colors.warningBg,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.xs,
                borderRadius: radius.sm,
                marginTop: spacing.md,
              }}
            >
              <Icon name="info" size={14} color={colors.warning} />
              <Text variant="caption" weight="600" color="warning" style={{ flex: 1 }}>
                Buyer&apos;s reason: {order.cancelReason}
              </Text>
            </View>
          )}
        </Card>

        {/* Bank payout — only for a seller paid into a bank account */}
        {order.payout ? (
          <Card style={{ marginBottom: spacing.md }}>
            <Text variant="bodyMed" weight="700">{payoutStageText(order.payout).title}</Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
              {payoutStageText(order.payout).body}
            </Text>
          </Card>
        ) : null}

        {/* Buyer */}
        <Card style={{ marginBottom: spacing.md }}>
          {CARD_LABEL('BUYER')}
          <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: spacing.sm }}>
            <Avatar name={buyerLabel} size={36} />
            <View style={{ flex: 1, marginLeft: spacing.md }}>
              <Text variant="bodyMed" weight="700">
                {buyerLabel}
              </Text>
              {order.buyer?.email && order.buyer?.email !== buyerLabel && (
                <Text variant="caption" color="textMuted">
                  {order.buyer.email}
                </Text>
              )}
            </View>
          </View>
        </Card>

        {/* Shipping address */}
        {shippingAddressParts.length > 0 && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('SHIP TO')}
            <View style={{ flexDirection: 'row', alignItems: 'flex-start', marginTop: spacing.sm }}>
              <Icon name="nearMe" size={18} color={colors.accent} />
              <View style={{ marginLeft: spacing.md, flex: 1, gap: 2 }}>
                {order.shippingRecipientName ? (
                  <Text variant="bodyMed" weight="600">
                    {order.shippingRecipientName}
                  </Text>
                ) : null}
                <Text variant="bodySm">{shippingAddressParts.join(', ')}</Text>
                {order.shippingPhone ? (
                  <Text variant="bodySm" color="textSecondary" selectable>
                    {order.shippingPhone}
                  </Text>
                ) : null}
              </View>
            </View>
          </Card>
        )}

        {/* Product */}
        {order.product && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('PRODUCT')}
            <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: spacing.sm }}>
              {order.product.imageUrl ? (
                <Image
                  source={{ uri: order.product.imageUrl }}
                  style={{ width: 56, height: 56, borderRadius: radius.sm, backgroundColor: colors.surfaceAlt }}
                />
              ) : (
                <View
                  style={{
                    width: 56,
                    height: 56,
                    borderRadius: radius.sm,
                    backgroundColor: colors.surfaceAlt,
                    justifyContent: 'center',
                    alignItems: 'center',
                  }}
                >
                  <Icon name="pricetag" size={24} color={colors.textMuted} />
                </View>
              )}
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodyMed" weight="700">
                  {order.product.name}
                </Text>
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginTop: spacing.xs }}>
                  <Text variant="caption" color="textMuted">
                    Qty: {order.quantity}
                  </Text>
                  <Text variant="caption" color="textMuted">
                    {formatPrice(order.product.price)} each
                  </Text>
                </View>
              </View>
            </View>
          </Card>
        )}

        {/* Amounts */}
        <Card style={{ marginBottom: spacing.md }}>
          {CARD_LABEL('PAYMENT')}
          <View style={{ marginTop: spacing.xs }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodySm" color="textSecondary">
                Product
              </Text>
              <Text variant="bodySm" numeric>
                {formatTokenAmount(order.productAmount, order.token)}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodySm" color="textSecondary">
                {isService ? 'Call-out fee' : 'Shipping'} ({formatStatus(order.shippingZone || '')})
              </Text>
              <Text variant="bodySm" numeric>
                {formatTokenAmount(shippingWithoutPayoutFee(order.shippingAmount, order.payoutFee), order.token)}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodySm" color="textSecondary">
                Platform fee (paid by you)
              </Text>
              <Text variant="bodySm" numeric>
                −{formatTokenAmount(order.platformFee, order.token)}
              </Text>
            </View>
            <Divider spacing="sm" />
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodyMed" weight="700">
                You receive
              </Text>
              <Text variant="bodyMed" weight="700" color="accent" numeric>
                {formatTokenAmount(sellerNet(order.totalAmount, order.platformFee), order.token)}
              </Text>
            </View>
          </View>
        </Card>

        {/* What the buyer booked — slot, units, booking-form answers (service orders) */}
        {order.booking && order.booking.lines.length > 0 && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('BOOKING DETAILS')}
            <View style={{ marginTop: spacing.xs }}>
              {order.booking.lines.map((l) => (
                <View
                  key={l.key}
                  style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', gap: spacing.md, paddingVertical: spacing.xs }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    {l.label}
                  </Text>
                  <Text variant="bodySm" weight="600" align="right" style={{ flex: 1 }}>
                    {l.value}
                  </Text>
                </View>
              ))}
            </View>
          </Card>
        )}

        {/* Buyer's arrangement — selected specs + purchase note (product orders) */}
        {((order.selectedAttributes && order.selectedAttributes.length > 0) || order.purchaseNote) && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('HOW TO ARRANGE')}
            <View style={{ marginTop: spacing.xs }}>
              {order.selectedAttributes?.map((a, i) => (
                <View
                  key={i}
                  style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    {a.name}
                  </Text>
                  <Text variant="bodySm" weight="600">
                    {a.value}
                  </Text>
                </View>
              ))}
              {order.purchaseNote ? (
                <>
                  <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm }}>
                    Buyer note
                  </Text>
                  <Text variant="bodySm" style={{ marginTop: spacing.xs }}>
                    {order.purchaseNote}
                  </Text>
                </>
              ) : null}
            </View>
          </Card>
        )}

        {/* Shipping info */}
        {(order.trackingNumber || order.notes) && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('SHIPPING INFO')}
            <View style={{ marginTop: spacing.xs }}>
              {order.trackingNumber && (
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
                  <Text variant="bodySm" color="textSecondary">
                    Tracking
                  </Text>
                  <Text variant="bodySm" weight="700" numeric>
                    {order.trackingNumber}
                  </Text>
                </View>
              )}
              {order.notes && (
                <>
                  <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm }}>
                    Notes
                  </Text>
                  <Text variant="bodySm" style={{ marginTop: spacing.xs }}>
                    {order.notes}
                  </Text>
                </>
              )}
            </View>
          </Card>
        )}

        {/* Timeline */}
        <Card style={{ marginBottom: spacing.md }}>
          {CARD_LABEL('TIMELINE')}
          <View style={{ marginTop: spacing.sm }}>
            <Stepper steps={timeline} />
          </View>
        </Card>

        {/* Blockchain */}
        {(order.escrowTxHash || order.releaseTxHash || order.contractOrderId) && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('BLOCKCHAIN')}
            <View style={{ marginTop: spacing.xs }}>
              {order.contractOrderId && (
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
                  <Text variant="bodySm" color="textSecondary">
                    Contract ID
                  </Text>
                  <Text variant="caption" numeric color="textSecondary" numberOfLines={1} ellipsizeMode="middle" style={{ maxWidth: 160 }}>
                    {order.contractOrderId}
                  </Text>
                </View>
              )}
              {order.escrowTxHash && (
                <PressableScale
                  onPress={() => openExplorer(order.escrowTxHash!)}
                  style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    Escrow tx
                  </Text>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <Text variant="caption" numeric color="textSecondary">
                      {order.escrowTxHash.slice(0, 10)}...
                    </Text>
                    <Icon name="arrowRight" size={14} color={colors.accent} />
                  </View>
                </PressableScale>
              )}
              {order.releaseTxHash && (
                <PressableScale
                  onPress={() => openExplorer(order.releaseTxHash!)}
                  style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    Release tx
                  </Text>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <Text variant="caption" numeric color="textSecondary">
                      {order.releaseTxHash.slice(0, 10)}...
                    </Text>
                    <Icon name="arrowRight" size={14} color={colors.accent} />
                  </View>
                </PressableScale>
              )}
            </View>
          </Card>
        )}

        {/* Delivery proof preview if exists */}
        {order.deliveryProofUrl && (
          <Card style={{ marginBottom: spacing.md }}>
            {CARD_LABEL('DELIVERY PROOF')}
            <View style={{ marginTop: spacing.sm }}>
              {isValidHttpUrl(order.deliveryProofUrl) ? (
                <Image
                  source={{ uri: order.deliveryProofUrl }}
                  style={{ width: '100%', height: 200, borderRadius: radius.md, backgroundColor: colors.surfaceAlt }}
                  resizeMode="cover"
                />
              ) : (
                <Text variant="bodySm" color="textMuted">
                  {order.deliveryProofUrl}
                </Text>
              )}
            </View>
          </Card>
        )}
      </ScrollView>

      {/* Dispute banner — overrides action bar when disputed */}
      {isDisputed && (
        <View
          style={{
            position: 'absolute',
            bottom: 0,
            left: 0,
            right: 0,
            backgroundColor: colors.dangerBg,
            padding: spacing.lg,
            borderTopWidth: 1,
            borderTopColor: colors.hairline,
          }}
        >
          <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
            <Icon name="dispute" size={18} color={colors.danger} />
            <View style={{ flex: 1, gap: spacing.xs }}>
              <Text variant="bodySm" weight="700" color="danger">
                Under admin review
              </Text>
              <Text variant="caption" color="danger">
                {order.disputeReason
                  ? `Reason: ${order.disputeReason}`
                  : 'Actions are paused until the admin resolves this dispute.'}
              </Text>
            </View>
          </View>
        </View>
      )}

      {/* Action bar */}
      {hasActionBar && (
        <View
          style={{
            position: 'absolute',
            bottom: 0,
            left: 0,
            right: 0,
            backgroundColor: colors.surface,
            padding: spacing.lg,
            borderTopWidth: 1,
            borderTopColor: colors.hairline,
            gap: spacing.sm,
          }}
        >
          {canShip && (
            <Button title="Mark as Shipped" leftIcon="send" onPress={() => setShipModalVisible(true)} />
          )}
          {canDeliver && (
            isService ? (
              <Button
                title="Mark service rendered"
                leftIcon="checkCircle"
                loading={actionLoading}
                disabled={actionLoading}
                onPress={handleMarkRendered}
              />
            ) : (
              <Button title="Mark as Delivered" leftIcon="checkCircle" onPress={() => setDeliverModalVisible(true)} />
            )
          )}
          {canRecordOnChain && (
            <Button
              title="Record delivery on-chain"
              leftIcon="checkCircle"
              loading={actionLoading}
              disabled={actionLoading}
              onPress={handleRecordOnChain}
            />
          )}
          {canResolveRefund && !order.merchantApprovedRefund && (
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button
                  title="Reject Refund"
                  variant="danger"
                  leftIcon="closeCircle"
                  onPress={() => { setRefundNotes(''); setRefundModalVisible('reject'); }}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  title="Approve Refund"
                  variant="success"
                  leftIcon="checkCircle"
                  onPress={() => { setRefundNotes(''); setRefundModalVisible('approve'); }}
                />
              </View>
            </View>
          )}
          {canHandleCancel && (
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button
                  title="Reject Cancel"
                  variant="danger"
                  leftIcon="closeCircle"
                  onPress={() => { setCancelNotes(''); setCancelModalVisible('reject'); }}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  title="Approve Cancel"
                  variant="success"
                  leftIcon="checkCircle"
                  onPress={() => { setCancelNotes(''); setCancelModalVisible('approve'); }}
                />
              </View>
            </View>
          )}
          {canRaiseDispute && (
            <PressableScale
              onPress={() => setDisputeModalVisible(true)}
              style={{ alignItems: 'center', paddingVertical: spacing.xs }}
            >
              <Text variant="bodySm" weight="600" color="accent">
                Report to Admin
              </Text>
            </PressableScale>
          )}
        </View>
      )}

      {/* Ship modal */}
      <Sheet visible={shipModalVisible} onClose={() => setShipModalVisible(false)} title="Mark as Shipped">
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              The buyer will be notified. Tracking info is optional.
            </Text>

            <Input
              label="Tracking number (optional)"
              value={trackingNumber}
              onChangeText={setTrackingNumber}
              placeholder="e.g. 1Z999AA10123456784"
              autoCapitalize="characters"
            />

            <Input
              label="Notes (optional)"
              value={shipNotes}
              onChangeText={setShipNotes}
              placeholder="Courier name, expected delivery date, etc."
              multiline
              inputStyle={{ height: 70 }}
            />

            <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button title="Cancel" variant="secondary" onPress={() => setShipModalVisible(false)} disabled={actionLoading} />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Confirm" onPress={handleShip} loading={actionLoading} disabled={actionLoading} />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>

      {/* Deliver modal — image upload */}
      <Sheet visible={deliverModalVisible} onClose={() => { setDeliverModalVisible(false); setProofImageUri(null); }} title="Mark as Delivered">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
          <Text variant="bodySm" color="textSecondary">
            Upload a photo of the delivery (signed receipt, package handed to buyer, etc.).
          </Text>

          {proofImageUri ? (
            <View style={{ position: 'relative' }}>
              <Image
                source={{ uri: proofImageUri }}
                style={{ width: '100%', height: 180, borderRadius: radius.md }}
                resizeMode="cover"
              />
              <IconButton
                icon="close"
                accessibilityLabel="Remove proof image"
                variant="surface"
                size={18}
                onPress={() => setProofImageUri(null)}
                style={{ position: 'absolute', top: spacing.sm, right: spacing.sm, width: 32, height: 32 }}
              />
            </View>
          ) : (
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <PressableScale
                onPress={captureProofPhoto}
                style={{
                  flex: 1,
                  paddingVertical: spacing.xl,
                  paddingHorizontal: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1.5,
                  borderStyle: 'dashed',
                  borderColor: colors.accent,
                  backgroundColor: colors.accentMuted,
                  alignItems: 'center',
                  gap: spacing.xs,
                }}
              >
                <Icon name="camera" size={24} color={colors.accent} />
                <Text variant="caption" weight="700" color="accent" align="center">
                  Take photo
                </Text>
              </PressableScale>
              <PressableScale
                onPress={pickProofImage}
                style={{
                  flex: 1,
                  paddingVertical: spacing.xl,
                  paddingHorizontal: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1.5,
                  borderStyle: 'dashed',
                  borderColor: colors.accent,
                  backgroundColor: colors.accentMuted,
                  alignItems: 'center',
                  gap: spacing.xs,
                }}
              >
                <Icon name="image" size={24} color={colors.accent} />
                <Text variant="caption" weight="700" color="accent" align="center">
                  Choose from gallery
                </Text>
              </PressableScale>
            </View>
          )}

          {uploadingProof && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Spinner inline label="Uploading proof…" />
            </View>
          )}

          <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.sm }}>
            <View style={{ flex: 1 }}>
              <Button
                title="Cancel"
                variant="secondary"
                onPress={() => { setDeliverModalVisible(false); setProofImageUri(null); }}
                disabled={actionLoading}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                title="Confirm"
                onPress={handleDeliver}
                loading={actionLoading}
                disabled={actionLoading || !proofImageUri}
              />
            </View>
          </View>
        </View>
      </Sheet>

      {/* Refund decision modal */}
      <Sheet
        visible={!!refundModalVisible}
        onClose={() => setRefundModalVisible(null)}
        title={refundModalVisible === 'approve' ? 'Approve Refund' : 'Reject Refund'}
      >
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              {refundModalVisible === 'approve'
                ? 'You agree to refund this buyer. An admin will finalize the refund on-chain. The order stays in refund-requested until then.'
                : 'You disagree with the refund request. The order will return to delivered status. The buyer can contact support if they disagree.'}
            </Text>

            <Input
              label="Notes for the buyer (optional)"
              value={refundNotes}
              onChangeText={setRefundNotes}
              placeholder="Reason for your decision..."
              multiline
              inputStyle={{ height: 90 }}
            />

            <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button title="Cancel" variant="secondary" onPress={() => setRefundModalVisible(null)} disabled={actionLoading} />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  title={refundModalVisible === 'approve' ? 'Approve' : 'Reject'}
                  variant={refundModalVisible === 'approve' ? 'success' : 'danger'}
                  onPress={handleRefundDecision}
                  loading={actionLoading}
                  disabled={actionLoading}
                />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>

      {/* Cancel decision modal */}
      <Sheet
        visible={!!cancelModalVisible}
        onClose={() => setCancelModalVisible(null)}
        title={cancelModalVisible === 'approve' ? 'Approve Cancel Request' : 'Reject Cancel Request'}
      >
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              {cancelModalVisible === 'approve'
                ? "You agree to cancel this order. You'll sign an on-chain transaction to return the buyer's funds."
                : 'You disagree with the cancel request. The order will return to escrowed status and you can proceed to ship.'}
            </Text>

            {cancelModalVisible === 'reject' && (
              <Input
                label="Note to the buyer (optional)"
                value={cancelNotes}
                onChangeText={setCancelNotes}
                placeholder="Why are you keeping the order active?"
                multiline
                inputStyle={{ height: 90 }}
              />
            )}

            <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button title="Back" variant="secondary" onPress={() => setCancelModalVisible(null)} disabled={actionLoading} />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  title={cancelModalVisible === 'approve' ? 'Approve & Sign' : 'Reject'}
                  variant={cancelModalVisible === 'approve' ? 'success' : 'danger'}
                  onPress={handleCancelDecision}
                  loading={actionLoading}
                  disabled={actionLoading}
                />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>

      {/* Dispute modal */}
      <Sheet
        visible={disputeModalVisible}
        onClose={() => { setDisputeModalVisible(false); setDisputeReason(''); }}
        title="Report to Admin"
      >
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              Describe the issue in detail (minimum 10 characters). An admin will review and resolve.
            </Text>
            <Input
              value={disputeReason}
              onChangeText={setDisputeReason}
              placeholder="What went wrong?"
              multiline
              inputStyle={{ height: 110 }}
            />
            <View style={{ flexDirection: 'row', gap: spacing.sm, marginTop: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Button
                  title="Cancel"
                  variant="secondary"
                  onPress={() => { setDisputeModalVisible(false); setDisputeReason(''); }}
                  disabled={actionLoading}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Submit Dispute" onPress={submitDispute} loading={actionLoading} disabled={actionLoading} />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>
    </SafeAreaView>
  );
};

export default MerchantOrderDetail;
