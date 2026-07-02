// app/(dashboard)/orderdetail.tsx
//
// Order detail — the marketplace buyer's view of a single escrow order, with the
// full status-gated action set (confirm receipt, request refund, request cancel,
// raise dispute, cancel order). Restyled onto the TSA Signature design system
// (@/theme + @/components/ui). ALL data fetching (getOrderById), the
// prepare→sign+broadcast→submit confirm flow, refund / cancel-request / dispute /
// cancel-order calls, status/dispute gating, money formatting (formatTokenAmount)
// and navigation are preserved verbatim — this is a visual/structural restyle only.
import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  ScrollView,
  Alert,
  TextInput,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import {
  getOrderById,
  prepareConfirm,
  submitConfirm,
  requestRefund,
  cancelOrder,
  requestCancelOrder,
  raiseDispute,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import { formatStatus, formatDate } from '@/constants/orderStatus';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Card,
  Icon,
  IconButton,
  PressableScale,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  useToneColors,
} from '@/components/ui';
import type { StatusTone } from '@/components/ui';

// Order statuses live in their own namespace (not trade/ad/dispute), so map them
// to the shared StatusTone palette here for token-driven coloring.
const ORDER_STATUS_TONE: Record<string, StatusTone> = {
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

const truncateHash = (hash?: string): string => {
  if (!hash) return '—';
  if (hash.length <= 16) return hash;
  return `${hash.slice(0, 10)}...${hash.slice(-6)}`;
};

const OrderDetailScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [order, setOrder] = useState<Order | null>(null);
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [cancelModalVisible, setCancelModalVisible] = useState(false);
  const [cancelReason, setCancelReason] = useState('');
  const [disputeModalVisible, setDisputeModalVisible] = useState(false);
  const [disputeReason, setDisputeReason] = useState('');

  const fetchOrder = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    const result = await getOrderById(id);
    if (result.success && result.data) {
      setOrder(result.data);
    } else {
      setError(result.message || 'Failed to load order');
    }
    setLoading(false);
  }, [id]);

  useEffect(() => {
    fetchOrder();
  }, [fetchOrder]);

  const handleConfirmReceipt = () => {
    const svc = order?.kind === 'service';
    Alert.alert(
      svc ? 'Confirm Service Completed' : 'Confirm Receipt',
      svc
        ? 'This will release funds to the provider. Are you sure the service was completed?'
        : 'This will release funds to the seller. Are you sure you received the item?',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Confirm',
          onPress: async () => {
            if (!order) return;
            setActionLoading(true);
            try {
              const prepResult = await prepareConfirm(order.id);
              if (!prepResult.success || !prepResult.data) {
                throw new Error(prepResult.message || 'Failed to prepare confirm');
              }

              const txHash = await signAndBroadcast(prepResult.data.confirmTx, 'sonic');

              const submitResult = await submitConfirm(order.id, txHash);
              if (!submitResult.success) {
                throw new Error(submitResult.message || 'Failed to submit confirm');
              }

              Alert.alert('Success', 'Receipt confirmed! Funds released to seller.');
              fetchOrder();
            } catch (err: any) {
              Alert.alert('Error', err.message || 'Failed to confirm receipt');
            } finally {
              setActionLoading(false);
            }
          },
        },
      ]
    );
  };

  const handleRequestRefund = () => {
    Alert.alert(
      'Request Refund',
      order?.status === 'escrowed'
        ? 'This will initiate an on-chain refund request.'
        : 'This will send your refund request for admin review.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Request Refund',
          style: 'destructive',
          onPress: async () => {
            if (!order) return;
            setActionLoading(true);
            try {
              const result = await requestRefund(order.id);
              if (!result.success) {
                throw new Error(result.message || 'Failed to request refund');
              }

              // If escrowed, we get a refundTx to sign and broadcast
              if (order.status === 'escrowed' && result.data?.refundTx) {
                const txHash = await signAndBroadcast(result.data.refundTx, 'sonic');
                Alert.alert(
                  'Refund Requested',
                  `On-chain refund request submitted.\nTx: ${txHash.slice(0, 16)}...`,
                );
              } else {
                Alert.alert('Success', 'Refund request submitted for review.');
              }
              fetchOrder();
            } catch (err: any) {
              Alert.alert('Error', err.message || 'Failed to request refund');
            } finally {
              setActionLoading(false);
            }
          },
        },
      ]
    );
  };

  const submitCancelRequest = async () => {
    if (!order) return;
    const reason = cancelReason.trim();
    if (reason.length < 3) {
      Alert.alert('Reason required', 'Please give a brief reason for the cancel request.');
      return;
    }
    setActionLoading(true);
    setCancelModalVisible(false);
    try {
      const result = await requestCancelOrder(order.id, reason);
      if (!result.success) throw new Error(result.message || 'Failed to submit cancel request');
      Alert.alert('Cancel Request Submitted', 'The seller will be notified to approve or reject.');
      setCancelReason('');
      fetchOrder();
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to submit cancel request');
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

  const handleCancelOrder = () => {
    if (!order) return;
    Alert.alert(
      'Cancel Order',
      'Are you sure you want to cancel this order? This cannot be undone.',
      [
        { text: 'Keep Order', style: 'cancel' },
        {
          text: 'Cancel Order',
          style: 'destructive',
          onPress: async () => {
            setActionLoading(true);
            try {
              const result = await cancelOrder(order.id);
              if (!result.success) {
                throw new Error(result.message || 'Failed to cancel order');
              }
              Alert.alert('Order Cancelled', 'Your order has been cancelled.');
              fetchOrder();
            } catch (err: any) {
              Alert.alert('Error', err.message || 'Failed to cancel order');
            } finally {
              setActionLoading(false);
            }
          },
        },
      ]
    );
  };

  const copyToClipboard = async (text: string) => {
    await Clipboard.setStringAsync(text);
    Alert.alert('Copied', 'Copied to clipboard');
  };

  const Header = () => (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
      }}
    >
      <IconButton
        icon="chevronLeft"
        variant="surface"
        accessibilityLabel="Go back"
        onPress={() => router.back()}
      />
      <Text variant="title3" weight="700">
        Order Detail
      </Text>
      <View style={{ width: 44 }} />
    </View>
  );

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <Spinner size="large" label="Loading order…" />
        </View>
      </SafeAreaView>
    );
  }

  if (error || !order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Header />
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <ErrorBody message={error || 'Order not found'} onRetry={fetchOrder} />
        </View>
      </SafeAreaView>
    );
  }

  const tone = ORDER_STATUS_TONE[order.status] ?? 'neutral';
  // Service orders skip the shipping leg: the call-out/booking fee rides in the
  // shipping amount, "delivered" reads as "rendered", and the buyer may confirm
  // completion straight from `escrowed` (confirmReceipt has no on-chain
  // delivered precondition — spec §6).
  const isService = order.kind === 'service';
  const feeLabel = isService ? 'Call-out fee' : 'Shipping';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Header />

      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ padding: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing['4xl'] }}
      >
        {/* Status badge */}
        <View style={{ alignItems: 'center', marginBottom: spacing.xl, gap: spacing.sm }}>
          <StatusBadge
            status={order.status}
            tone={tone}
            label={isService && order.status === 'delivered' ? 'Rendered' : undefined}
          />
          {order.status === 'completed' && (
            <Icon name="checkCircle" size={32} color={colors.success} />
          )}
        </View>

        {/* Order info */}
        <Card style={{ marginBottom: spacing.lg }}>
          <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
            Order Info
          </Text>
          <InfoRow label="Order ID" value={order.id.slice(0, 16) + '...'} onCopy={() => copyToClipboard(order.id)} />
          <InfoRow label="Quantity" value={String(order.quantity)} />
          <InfoRow label="Token" value={order.token} />
          <InfoRow label="Shipping Zone" value={formatStatus(order.shippingZone || 'N/A')} />
        </Card>

        {/* Selected specs + purchase note (product orders) */}
        {((order.selectedAttributes && order.selectedAttributes.length > 0) || order.purchaseNote) && (
          <Card style={{ marginBottom: spacing.lg }}>
            <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
              Your selections
            </Text>
            {order.selectedAttributes?.map((a, i) => (
              <InfoRow key={i} label={a.name} value={a.value} />
            ))}
            {order.purchaseNote ? (
              <View style={{ marginTop: spacing.sm }}>
                <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
                  Note to seller
                </Text>
                <Text variant="bodySm">{order.purchaseNote}</Text>
              </View>
            ) : null}
          </Card>
        )}

        {/* Amounts */}
        <Card style={{ marginBottom: spacing.lg }}>
          <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
            Amounts
          </Text>
          <SummaryCard dividerBeforeLast>
            <SummaryRow label={isService ? 'Service' : 'Product'} value={formatTokenAmount(order.productAmount, order.token)} />
            <SummaryRow label={feeLabel} value={formatTokenAmount(order.shippingAmount, order.token)} />
            <SummaryRow label="Platform Fee" value={formatTokenAmount(order.platformFee, order.token)} />
            <SummaryRow label="Total" value={formatTokenAmount(order.totalAmount, order.token)} total />
          </SummaryCard>
        </Card>

        {/* Blockchain info */}
        {(order.contractOrderId || order.escrowTxHash || order.approveTxHash || order.releaseTxHash) && (
          <Card style={{ marginBottom: spacing.lg }}>
            <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
              Blockchain
            </Text>
            {order.contractOrderId && (
              <InfoRow
                label="Contract Order"
                value={truncateHash(order.contractOrderId)}
                onCopy={() => copyToClipboard(order.contractOrderId!)}
              />
            )}
            {order.approveTxHash && (
              <InfoRow
                label="Approve Tx"
                value={truncateHash(order.approveTxHash)}
                onCopy={() => copyToClipboard(order.approveTxHash!)}
              />
            )}
            {order.escrowTxHash && (
              <InfoRow
                label="Escrow Tx"
                value={truncateHash(order.escrowTxHash)}
                onCopy={() => copyToClipboard(order.escrowTxHash!)}
              />
            )}
            {order.releaseTxHash && (
              <InfoRow
                label="Release Tx"
                value={truncateHash(order.releaseTxHash)}
                onCopy={() => copyToClipboard(order.releaseTxHash!)}
              />
            )}
          </Card>
        )}

        {/* Timeline */}
        <Card style={{ marginBottom: spacing.lg }}>
          <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
            Timeline
          </Text>
          <InfoRow label="Created" value={formatDate(order.createdAt, true)} />
          {order.sellerDeliveredAt && (
            <InfoRow label={isService ? 'Rendered' : 'Delivered'} value={formatDate(order.sellerDeliveredAt, true)} />
          )}
          {order.buyerConfirmedAt && (
            <InfoRow label="Confirmed" value={formatDate(order.buyerConfirmedAt, true)} />
          )}
          {order.escrowExpiresAt && (
            <InfoRow label="Escrow Expires" value={formatDate(order.escrowExpiresAt, true)} />
          )}
        </Card>

        {/* Action buttons */}
        {actionLoading ? (
          <View style={{ alignItems: 'center', paddingVertical: spacing['2xl'] }}>
            <Spinner size="large" label="Processing transaction…" />
          </View>
        ) : order.disputedAt ? (
          <View style={{ gap: spacing.md }}>
            <Banner
              tone="danger"
              icon="dispute"
              text="This order is under admin review. Actions are paused until a resolution."
            />
            {order.disputeReason ? (
              <Text variant="caption" color="textMuted" style={{ fontStyle: 'italic', paddingHorizontal: spacing.xs }}>
                Reason: {order.disputeReason}
              </Text>
            ) : null}
          </View>
        ) : (
          <>
            {order.status === 'escrowed' && (
              <View style={{ gap: spacing.md }}>
                <Banner
                  tone="info"
                  icon="clock"
                  text={isService ? 'Payment escrowed — waiting for the provider to perform the service' : 'Waiting for seller to ship'}
                />
                {/* Services can be confirmed straight from escrowed (no shipping
                    leg). For products the buyer waits for ship/deliver first. */}
                {isService && (
                  <Button
                    title="Confirm service completed"
                    variant="success"
                    leftIcon="checkCircle"
                    size="lg"
                    onPress={handleConfirmReceipt}
                  />
                )}
                <Button
                  title="Request Cancel"
                  variant="danger"
                  onPress={() => setCancelModalVisible(true)}
                />
                <Button title="Request Refund" variant="danger" onPress={handleRequestRefund} />
                <Button
                  title="Report to Admin"
                  variant="ghost"
                  size="sm"
                  onPress={() => setDisputeModalVisible(true)}
                />
              </View>
            )}

            {order.status === 'cancel_requested' && (
              <View style={{ gap: spacing.md }}>
                <Banner tone="warning" icon="clock" text="Your cancel request is pending seller approval." />
                {order.cancelReason ? (
                  <Text variant="caption" color="textMuted" style={{ fontStyle: 'italic', paddingHorizontal: spacing.xs }}>
                    Your reason: {order.cancelReason}
                  </Text>
                ) : null}
                <Button
                  title="Report to Admin"
                  variant="ghost"
                  size="sm"
                  onPress={() => setDisputeModalVisible(true)}
                />
              </View>
            )}

            {order.status === 'shipped' && (
              <View style={{ gap: spacing.md }}>
                <Banner
                  tone="info"
                  icon="storefront"
                  text={
                    order.trackingNumber
                      ? `Shipped! Tracking: ${order.trackingNumber}`
                      : 'Your order has been shipped!'
                  }
                />
                <Button
                  title="Mark as Received"
                  variant="success"
                  leftIcon="checkCircle"
                  size="lg"
                  onPress={handleConfirmReceipt}
                />
                <Button title="Request Refund" variant="danger" onPress={handleRequestRefund} />
                <Button
                  title="Report to Admin"
                  variant="ghost"
                  size="sm"
                  onPress={() => setDisputeModalVisible(true)}
                />
              </View>
            )}

            {order.status === 'delivered' && (
              <View style={{ gap: spacing.md }}>
                {isService && (
                  <Banner tone="success" icon="checkCircle" text="Service rendered by the provider. Confirm to release funds." />
                )}
                <Button
                  title={isService ? 'Confirm service completed' : 'Confirm Receipt'}
                  variant="success"
                  leftIcon="checkCircle"
                  size="lg"
                  onPress={handleConfirmReceipt}
                />
                <Button title="Request Refund" variant="danger" onPress={handleRequestRefund} />
                <Button
                  title="Report to Admin"
                  variant="ghost"
                  size="sm"
                  onPress={() => setDisputeModalVisible(true)}
                />
              </View>
            )}

            {order.status === 'refund_requested' && (
              <View style={{ gap: spacing.md }}>
                <Banner tone="warning" icon="clock" text="Refund requested. Awaiting seller or admin review." />
                <Button
                  title="Report to Admin"
                  variant="ghost"
                  size="sm"
                  onPress={() => setDisputeModalVisible(true)}
                />
              </View>
            )}

            {order.status === 'pending_payment' && (
              <View style={{ gap: spacing.md }}>
                <Banner tone="warning" icon="wallet" text="Awaiting payment. Complete checkout to fund escrow." />
                <Button title="Cancel Order" variant="danger" onPress={handleCancelOrder} />
              </View>
            )}
          </>
        )}
      </ScrollView>

      {/* Cancel request modal */}
      <Sheet
        visible={cancelModalVisible}
        onClose={() => setCancelModalVisible(false)}
        title="Request to Cancel"
      >
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              Tell the seller why you want to cancel. They will approve or reject the request.
            </Text>
            <TextInput
              style={{
                borderWidth: 1.5,
                borderColor: colors.border,
                borderRadius: radius.md,
                padding: spacing.md,
                fontSize: 14,
                color: colors.textPrimary,
                minHeight: 70,
                textAlignVertical: 'top',
                backgroundColor: colors.surface,
              }}
              placeholder="e.g. Ordered the wrong item"
              placeholderTextColor={colors.textMuted}
              value={cancelReason}
              onChangeText={setCancelReason}
              multiline
            />
            <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.xs }}>
              <View style={{ flex: 1 }}>
                <Button
                  title="Keep Order"
                  variant="secondary"
                  onPress={() => {
                    setCancelModalVisible(false);
                    setCancelReason('');
                  }}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Submit Request" onPress={submitCancelRequest} />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>

      {/* Dispute modal */}
      <Sheet
        visible={disputeModalVisible}
        onClose={() => setDisputeModalVisible(false)}
        title="Report to Admin"
      >
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              Describe the issue in detail (minimum 10 characters). An admin will review and resolve.
            </Text>
            <TextInput
              style={{
                borderWidth: 1.5,
                borderColor: colors.border,
                borderRadius: radius.md,
                padding: spacing.md,
                fontSize: 14,
                color: colors.textPrimary,
                minHeight: 90,
                textAlignVertical: 'top',
                backgroundColor: colors.surface,
              }}
              placeholder="What went wrong?"
              placeholderTextColor={colors.textMuted}
              value={disputeReason}
              onChangeText={setDisputeReason}
              multiline
            />
            <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.xs }}>
              <View style={{ flex: 1 }}>
                <Button
                  title="Cancel"
                  variant="secondary"
                  onPress={() => {
                    setDisputeModalVisible(false);
                    setDisputeReason('');
                  }}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Submit Dispute" onPress={submitDispute} />
              </View>
            </View>
          </View>
        </KeyboardAvoidingView>
      </Sheet>
    </SafeAreaView>
  );
};

// Large status badge driven by the shared StatusTone palette.
const StatusBadge = ({ status, tone, label }: { status: string; tone: StatusTone; label?: string }) => {
  const { color, bg } = useToneColors(tone);
  const { spacing } = useTheme();
  return (
    <Badge
      label={label ?? formatStatus(status)}
      color={color}
      background={bg}
      style={{ paddingHorizontal: spacing.xl, paddingVertical: spacing.sm }}
    />
  );
};

// Tone-colored info/warning banner (replaces the legacy waiting banner).
const Banner = ({
  tone,
  icon,
  text,
}: {
  tone: StatusTone;
  icon: React.ComponentProps<typeof Icon>['name'];
  text: string;
}) => {
  const { color } = useToneColors(tone);
  const { spacing } = useTheme();
  return (
    <AccentCard railColor={color} padding="md">
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
        <Icon name={icon} size={20} color={color} />
        <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
          {text}
        </Text>
      </View>
    </AccentCard>
  );
};

// Inline error body for the failed/empty fetch state.
const ErrorBody = ({ message, onRetry }: { message: string; onRetry: () => void }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ alignItems: 'center', paddingHorizontal: spacing.xl, gap: spacing.md }}>
      <Icon name="warning" size={48} color={colors.danger} />
      <Text variant="body" color="textSecondary" align="center">
        {message}
      </Text>
      <Button title="Try Again" variant="secondary" leftIcon="refresh" fullWidth={false} onPress={onRetry} />
    </View>
  );
};

// Reusable info row — label on the left, value (with optional copy) on the right.
const InfoRow = ({
  label,
  value,
  onCopy,
}: {
  label: string;
  value: string;
  onCopy?: () => void;
}) => {
  const { colors, spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingVertical: spacing.sm,
      }}
    >
      <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
        {label}
      </Text>
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.xs,
          flex: 1,
          justifyContent: 'flex-end',
        }}
      >
        <Text variant="bodySm" weight="600" numeric align="right">
          {value}
        </Text>
        {onCopy && (
          <PressableScale onPress={onCopy} accessibilityRole="button" accessibilityLabel={`Copy ${label}`} style={{ padding: spacing.xs }}>
            <Icon name="copy" size={14} color={colors.accent} />
          </PressableScale>
        )}
      </View>
    </View>
  );
};

export default OrderDetailScreen;
