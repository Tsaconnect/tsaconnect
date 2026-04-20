import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
  Modal,
  TextInput,
  Linking,
  Image,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router, useLocalSearchParams } from 'expo-router';
import * as ImagePicker from 'expo-image-picker';
import {
  getOrderById,
  markOrderShipped,
  markOrderDelivered,
  approveRefund,
  rejectRefund,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import api from '@/components/services/api';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

const GOLD = '#D4AF37';

// Sonic explorer for txs (the wallet currently only supports Sonic for these flows)
const TX_EXPLORER_URL = 'https://sonicscan.org/tx/';

// Steps in the order lifecycle, in display order. Used by the timeline.
type TimelineStep = {
  key: string;
  label: string;
  timestamp?: string;
  isCurrent?: boolean;
  isComplete?: boolean;
};

function buildTimeline(order: Order): TimelineStep[] {
  const isRefunded = order.status === 'refunded';
  const isCancelled = order.status === 'cancelled';
  const isRefundRequested = order.status === 'refund_requested';

  // Refund / cancel branches replace later steps
  if (isCancelled) {
    return [
      { key: 'created', label: 'Order Placed', timestamp: order.createdAt, isComplete: true },
      { key: 'cancelled', label: 'Cancelled', timestamp: order.updatedAt, isCurrent: true, isComplete: true },
    ];
  }

  const steps: TimelineStep[] = [
    { key: 'created', label: 'Order Placed', timestamp: order.createdAt, isComplete: true },
    {
      key: 'escrowed',
      label: 'Payment Escrowed',
      timestamp: order.escrowExpiresAt ? undefined : undefined, // backend doesn't store an escrow timestamp explicitly
      isComplete: ['escrowed', 'shipped', 'delivered', 'completed', 'refund_requested', 'refunded'].includes(order.status),
    },
    {
      key: 'shipped',
      label: 'Shipped',
      timestamp: order.sellerShippedAt,
      isComplete: ['shipped', 'delivered', 'completed', 'refund_requested', 'refunded'].includes(order.status),
    },
    {
      key: 'delivered',
      label: 'Delivered',
      timestamp: order.sellerDeliveredAt,
      isComplete: ['delivered', 'completed', 'refund_requested', 'refunded'].includes(order.status),
    },
    {
      key: 'completed',
      label: 'Completed',
      timestamp: order.buyerConfirmedAt,
      isComplete: order.status === 'completed',
    },
  ];

  if (isRefundRequested || isRefunded) {
    steps.push({
      key: 'refund_requested',
      label: 'Refund Requested',
      isComplete: true,
    });
    if (isRefunded) {
      steps.push({ key: 'refunded', label: 'Refunded', timestamp: order.updatedAt, isComplete: true, isCurrent: true });
    }
  }

  // Mark the latest non-complete step or the current status as current
  const currentIdx = steps.findIndex((s) => s.key === order.status);
  if (currentIdx >= 0) steps[currentIdx].isCurrent = true;

  return steps;
}

function isValidHttpUrl(value: string): boolean {
  try {
    const u = new URL(value);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch {
    return false;
  }
}

const MerchantOrderDetail = () => {
  const { orderId } = useLocalSearchParams<{ orderId: string }>();
  const [order, setOrder] = useState<Order | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [orderFetchFailed, setOrderFetchFailed] = useState(false);
  const [actionLoading, setActionLoading] = useState(false);

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

      const result = await markOrderDelivered(order.id, proofUrl);
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
      <View style={styles.centered}>
        <ActivityIndicator size="large" color={GOLD} />
      </View>
    );
  }

  if (orderFetchFailed || !order) {
    return (
      <View style={styles.centered}>
        <Ionicons name="alert-circle-outline" size={64} color="#D9B68B" />
        <Text style={styles.errorTitle}>{error || 'Order not found'}</Text>
        <View style={{ flexDirection: 'row', gap: 10, marginTop: 16 }}>
          <TouchableOpacity style={styles.retryBtn} onPress={() => { setLoading(true); fetchOrder().finally(() => setLoading(false)); }}>
            <Text style={styles.retryText}>Retry</Text>
          </TouchableOpacity>
          <TouchableOpacity style={[styles.retryBtn, { backgroundColor: '#888' }]} onPress={() => router.back()}>
            <Text style={styles.retryText}>Go back</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  const statusColor = STATUS_COLORS[order.status] || STATUS_COLORS.cancelled;
  const canShip = order.status === 'escrowed';
  const canDeliver = order.status === 'shipped';
  const canResolveRefund = order.status === 'refund_requested';
  const timeline = buildTimeline(order);
  const buyerLabel =
    order.buyer?.name?.trim() ||
    (order.buyer?.username ? `@${order.buyer.username}` : '') ||
    order.buyer?.email ||
    'Unknown buyer';
  const shippingAddressParts = [order.shippingCity, order.shippingState, order.shippingCountry].filter(Boolean);

  return (
    <View style={{ flex: 1, backgroundColor: '#FAFAFA' }}>
      <View style={styles.header}>
        <TouchableOpacity onPress={() => router.back()} style={styles.backBtn}>
          <Ionicons name="chevron-back" size={24} color="#1A1A1A" />
        </TouchableOpacity>
        <Text style={styles.title}>Order Detail</Text>
        <View style={{ width: 40 }} />
      </View>

      <ScrollView contentContainerStyle={styles.content}>
        {/* Status card */}
        <View style={[styles.card, styles.statusCard]}>
          <View style={[styles.statusBadge, { backgroundColor: statusColor.bg }]}>
            <Text style={[styles.statusText, { color: statusColor.text }]}>
              {formatStatus(order.status)}
            </Text>
          </View>
          <Text style={styles.orderIdText}>Order #{order.id.slice(0, 8)}</Text>
          {order.merchantApprovedRefund && (
            <View style={styles.refundFlag}>
              <Ionicons name="checkmark-circle" size={14} color="#16A34A" />
              <Text style={styles.refundFlagText}>You approved this refund. Awaiting admin.</Text>
            </View>
          )}
        </View>

        {/* Buyer */}
        <View style={styles.card}>
          <Text style={styles.cardLabel}>BUYER</Text>
          <View style={styles.rowStart}>
            <View style={styles.avatar}>
              <Ionicons name="person" size={18} color={GOLD} />
            </View>
            <View style={{ flex: 1 }}>
              <Text style={styles.buyerName}>{buyerLabel}</Text>
              {order.buyer?.email && order.buyer?.email !== buyerLabel && (
                <Text style={styles.muted}>{order.buyer.email}</Text>
              )}
            </View>
          </View>
        </View>

        {/* Shipping address */}
        {shippingAddressParts.length > 0 && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>SHIP TO</Text>
            <View style={styles.rowStart}>
              <Ionicons name="location" size={18} color={GOLD} />
              <Text style={{ marginLeft: 10, flex: 1, fontSize: 14, color: '#1A1A1A' }}>
                {shippingAddressParts.join(', ')}
              </Text>
            </View>
          </View>
        )}

        {/* Product */}
        {order.product && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>PRODUCT</Text>
            <View style={styles.rowStart}>
              {order.product.imageUrl ? (
                <Image source={{ uri: order.product.imageUrl }} style={styles.productImage} />
              ) : (
                <View style={[styles.productImage, styles.productImagePlaceholder]}>
                  <Ionicons name="cube-outline" size={24} color="#CCC" />
                </View>
              )}
              <View style={{ flex: 1, marginLeft: 12 }}>
                <Text style={styles.productName}>{order.product.name}</Text>
                <View style={[styles.rowBetween, { marginTop: 4 }]}>
                  <Text style={styles.muted}>Qty: {order.quantity}</Text>
                  <Text style={styles.muted}>${order.product.price.toFixed(2)} each</Text>
                </View>
              </View>
            </View>
          </View>
        )}

        {/* Amounts */}
        <View style={styles.card}>
          <Text style={styles.cardLabel}>PAYMENT</Text>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Product</Text>
            <Text>{formatTokenAmount(order.productAmount, order.token)}</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Shipping ({formatStatus(order.shippingZone || '')})</Text>
            <Text>{formatTokenAmount(order.shippingAmount, order.token)}</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Platform fee</Text>
            <Text>{formatTokenAmount(order.platformFee, order.token)}</Text>
          </View>
          <View style={[styles.rowBetween, styles.divider]}>
            <Text style={styles.bold}>Total</Text>
            <Text style={[styles.bold, { color: GOLD }]}>
              {formatTokenAmount(order.totalAmount, order.token)}
            </Text>
          </View>
        </View>

        {/* Shipping info */}
        {(order.trackingNumber || order.notes) && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>SHIPPING INFO</Text>
            {order.trackingNumber && (
              <View style={styles.rowBetween}>
                <Text style={styles.muted}>Tracking</Text>
                <Text style={styles.bold}>{order.trackingNumber}</Text>
              </View>
            )}
            {order.notes && (
              <>
                <Text style={[styles.muted, { marginTop: 8 }]}>Notes</Text>
                <Text style={{ marginTop: 4 }}>{order.notes}</Text>
              </>
            )}
          </View>
        )}

        {/* Timeline */}
        <View style={styles.card}>
          <Text style={styles.cardLabel}>TIMELINE</Text>
          {timeline.map((step, i) => {
            const isLast = i === timeline.length - 1;
            return (
              <View key={step.key} style={styles.timelineRow}>
                <View style={styles.timelineLeft}>
                  <View
                    style={[
                      styles.timelineDot,
                      step.isCurrent && styles.timelineDotCurrent,
                      step.isComplete && !step.isCurrent && styles.timelineDotComplete,
                    ]}
                  >
                    {step.isComplete && <Ionicons name="checkmark" size={12} color="#FFF" />}
                  </View>
                  {!isLast && (
                    <View
                      style={[
                        styles.timelineLine,
                        step.isComplete && styles.timelineLineComplete,
                      ]}
                    />
                  )}
                </View>
                <View style={styles.timelineRight}>
                  <Text style={[styles.timelineLabel, step.isCurrent && styles.timelineLabelCurrent]}>
                    {step.label}
                  </Text>
                  {step.timestamp && (
                    <Text style={styles.timelineTime}>{formatDate(step.timestamp, true)}</Text>
                  )}
                </View>
              </View>
            );
          })}
        </View>

        {/* Blockchain */}
        {(order.escrowTxHash || order.releaseTxHash || order.contractOrderId) && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>BLOCKCHAIN</Text>
            {order.contractOrderId && (
              <View style={styles.rowBetween}>
                <Text style={styles.muted}>Contract ID</Text>
                <Text style={styles.hash} numberOfLines={1} ellipsizeMode="middle">
                  {order.contractOrderId}
                </Text>
              </View>
            )}
            {order.escrowTxHash && (
              <TouchableOpacity style={styles.txRow} onPress={() => openExplorer(order.escrowTxHash!)}>
                <Text style={styles.muted}>Escrow tx</Text>
                <View style={styles.txLink}>
                  <Text style={styles.hash} numberOfLines={1} ellipsizeMode="middle">
                    {order.escrowTxHash.slice(0, 10)}...
                  </Text>
                  <Ionicons name="open-outline" size={14} color={GOLD} />
                </View>
              </TouchableOpacity>
            )}
            {order.releaseTxHash && (
              <TouchableOpacity style={styles.txRow} onPress={() => openExplorer(order.releaseTxHash!)}>
                <Text style={styles.muted}>Release tx</Text>
                <View style={styles.txLink}>
                  <Text style={styles.hash} numberOfLines={1} ellipsizeMode="middle">
                    {order.releaseTxHash.slice(0, 10)}...
                  </Text>
                  <Ionicons name="open-outline" size={14} color={GOLD} />
                </View>
              </TouchableOpacity>
            )}
          </View>
        )}

        {/* Delivery proof preview if exists */}
        {order.deliveryProofUrl && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>DELIVERY PROOF</Text>
            {isValidHttpUrl(order.deliveryProofUrl) ? (
              <Image source={{ uri: order.deliveryProofUrl }} style={styles.proofImage} resizeMode="cover" />
            ) : (
              <Text style={styles.muted}>{order.deliveryProofUrl}</Text>
            )}
          </View>
        )}
      </ScrollView>

      {/* Action bar */}
      {(canShip || canDeliver || canResolveRefund) && (
        <View style={styles.actionBar}>
          {canShip && (
            <TouchableOpacity style={[styles.actionBtn, { backgroundColor: GOLD }]} onPress={() => setShipModalVisible(true)}>
              <Ionicons name="send" size={18} color="#FFF" />
              <Text style={styles.actionBtnText}>Mark as Shipped</Text>
            </TouchableOpacity>
          )}
          {canDeliver && (
            <TouchableOpacity style={[styles.actionBtn, { backgroundColor: GOLD }]} onPress={() => setDeliverModalVisible(true)}>
              <Ionicons name="checkmark-circle" size={18} color="#FFF" />
              <Text style={styles.actionBtnText}>Mark as Delivered</Text>
            </TouchableOpacity>
          )}
          {canResolveRefund && !order.merchantApprovedRefund && (
            <View style={styles.refundActions}>
              <TouchableOpacity
                style={[styles.actionBtn, styles.refundRejectBtn]}
                onPress={() => { setRefundNotes(''); setRefundModalVisible('reject'); }}
              >
                <Ionicons name="close-circle-outline" size={18} color="#DC2626" />
                <Text style={[styles.actionBtnText, { color: '#DC2626' }]}>Reject Refund</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[styles.actionBtn, { backgroundColor: '#16A34A' }]}
                onPress={() => { setRefundNotes(''); setRefundModalVisible('approve'); }}
              >
                <Ionicons name="checkmark-circle-outline" size={18} color="#FFF" />
                <Text style={styles.actionBtnText}>Approve Refund</Text>
              </TouchableOpacity>
            </View>
          )}
        </View>
      )}

      {/* Ship modal */}
      <Modal visible={shipModalVisible} transparent animationType="slide">
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>Mark as Shipped</Text>
            <Text style={styles.modalHint}>The buyer will be notified. Tracking info is optional.</Text>

            <Text style={styles.inputLabel}>Tracking number (optional)</Text>
            <TextInput
              style={styles.input}
              value={trackingNumber}
              onChangeText={setTrackingNumber}
              placeholder="e.g. 1Z999AA10123456784"
              placeholderTextColor="#BBB"
              autoCapitalize="characters"
            />

            <Text style={styles.inputLabel}>Notes (optional)</Text>
            <TextInput
              style={[styles.input, { height: 70 }]}
              value={shipNotes}
              onChangeText={setShipNotes}
              placeholder="Courier name, expected delivery date, etc."
              placeholderTextColor="#BBB"
              multiline
            />

            <View style={styles.modalActions}>
              <TouchableOpacity style={[styles.modalBtn, styles.modalBtnSecondary]} onPress={() => setShipModalVisible(false)} disabled={actionLoading}>
                <Text style={styles.modalBtnSecondaryText}>Cancel</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[styles.modalBtn, styles.modalBtnPrimary, actionLoading && { opacity: 0.5 }]}
                onPress={handleShip}
                disabled={actionLoading}
              >
                {actionLoading ? <ActivityIndicator color="#FFF" size="small" /> : <Text style={styles.modalBtnPrimaryText}>Confirm</Text>}
              </TouchableOpacity>
            </View>
          </View>
        </View>
        </KeyboardAvoidingView>
      </Modal>

      {/* Deliver modal — image upload */}
      <Modal visible={deliverModalVisible} transparent animationType="slide">
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>Mark as Delivered</Text>
            <Text style={styles.modalHint}>Upload a photo of the delivery (signed receipt, package handed to buyer, etc.).</Text>

            {proofImageUri ? (
              <View style={styles.proofPreviewWrap}>
                <Image source={{ uri: proofImageUri }} style={styles.proofPreview} resizeMode="cover" />
                <TouchableOpacity style={styles.removeProofBtn} onPress={() => setProofImageUri(null)}>
                  <Ionicons name="close" size={18} color="#FFF" />
                </TouchableOpacity>
              </View>
            ) : (
              <View style={styles.proofPickerRow}>
                <TouchableOpacity style={styles.proofPickerBtn} onPress={captureProofPhoto}>
                  <Ionicons name="camera" size={24} color={GOLD} />
                  <Text style={styles.proofPickerText}>Take photo</Text>
                </TouchableOpacity>
                <TouchableOpacity style={styles.proofPickerBtn} onPress={pickProofImage}>
                  <Ionicons name="image" size={24} color={GOLD} />
                  <Text style={styles.proofPickerText}>Choose from gallery</Text>
                </TouchableOpacity>
              </View>
            )}

            {uploadingProof && (
              <View style={styles.uploadingRow}>
                <ActivityIndicator color={GOLD} size="small" />
                <Text style={styles.muted}>Uploading proof...</Text>
              </View>
            )}

            <View style={styles.modalActions}>
              <TouchableOpacity
                style={[styles.modalBtn, styles.modalBtnSecondary]}
                onPress={() => { setDeliverModalVisible(false); setProofImageUri(null); }}
                disabled={actionLoading}
              >
                <Text style={styles.modalBtnSecondaryText}>Cancel</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[styles.modalBtn, styles.modalBtnPrimary, (actionLoading || !proofImageUri) && { opacity: 0.5 }]}
                onPress={handleDeliver}
                disabled={actionLoading || !proofImageUri}
              >
                {actionLoading ? <ActivityIndicator color="#FFF" size="small" /> : <Text style={styles.modalBtnPrimaryText}>Confirm</Text>}
              </TouchableOpacity>
            </View>
          </View>
        </View>
        </KeyboardAvoidingView>
      </Modal>

      {/* Refund decision modal */}
      <Modal visible={!!refundModalVisible} transparent animationType="slide">
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>
              {refundModalVisible === 'approve' ? 'Approve Refund' : 'Reject Refund'}
            </Text>
            <Text style={styles.modalHint}>
              {refundModalVisible === 'approve'
                ? 'You agree to refund this buyer. An admin will finalize the refund on-chain. The order stays in refund-requested until then.'
                : 'You disagree with the refund request. The order will return to delivered status. The buyer can contact support if they disagree.'}
            </Text>

            <Text style={styles.inputLabel}>Notes for the buyer (optional)</Text>
            <TextInput
              style={[styles.input, { height: 90 }]}
              value={refundNotes}
              onChangeText={setRefundNotes}
              placeholder="Reason for your decision..."
              placeholderTextColor="#BBB"
              multiline
            />

            <View style={styles.modalActions}>
              <TouchableOpacity style={[styles.modalBtn, styles.modalBtnSecondary]} onPress={() => setRefundModalVisible(null)} disabled={actionLoading}>
                <Text style={styles.modalBtnSecondaryText}>Cancel</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[
                  styles.modalBtn,
                  refundModalVisible === 'approve' ? { backgroundColor: '#16A34A' } : { backgroundColor: '#DC2626' },
                  actionLoading && { opacity: 0.5 },
                ]}
                onPress={handleRefundDecision}
                disabled={actionLoading}
              >
                {actionLoading ? (
                  <ActivityIndicator color="#FFF" size="small" />
                ) : (
                  <Text style={styles.modalBtnPrimaryText}>{refundModalVisible === 'approve' ? 'Approve' : 'Reject'}</Text>
                )}
              </TouchableOpacity>
            </View>
          </View>
        </View>
        </KeyboardAvoidingView>
      </Modal>
    </View>
  );
};

export default MerchantOrderDetail;

const styles = StyleSheet.create({
  centered: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 32 },
  errorTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A', marginTop: 16, textAlign: 'center' },

  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: '#FFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  backBtn: { width: 40, height: 40, justifyContent: 'center', alignItems: 'center' },
  title: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },

  content: { padding: 16, paddingBottom: 140 },

  card: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  statusCard: { alignItems: 'center' },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 10 },

  statusBadge: { paddingHorizontal: 14, paddingVertical: 6, borderRadius: 14, marginBottom: 10 },
  statusText: { fontSize: 13, fontWeight: '700', letterSpacing: 0.5 },
  orderIdText: { fontSize: 13, color: '#666' },
  refundFlag: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    backgroundColor: '#F0FDF4',
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 10,
    marginTop: 10,
  },
  refundFlagText: { fontSize: 12, color: '#166534', fontWeight: '600' },

  rowStart: { flexDirection: 'row', alignItems: 'center' },
  avatar: {
    width: 36, height: 36, borderRadius: 18,
    backgroundColor: '#FFF8E1',
    justifyContent: 'center', alignItems: 'center',
    marginRight: 12,
  },
  buyerName: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },

  productImage: { width: 56, height: 56, borderRadius: 8, backgroundColor: '#F0F0F0' },
  productImagePlaceholder: { justifyContent: 'center', alignItems: 'center' },
  productName: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },

  rowBetween: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 5 },
  muted: { color: '#888', fontSize: 13 },
  bold: { fontWeight: '700', color: '#1A1A1A', fontSize: 13 },
  divider: { marginTop: 8, paddingTop: 12, borderTopWidth: 1, borderTopColor: '#F0F0F0' },

  hash: { fontSize: 12, color: '#555', fontFamily: 'monospace', maxWidth: 160 },
  txRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 6 },
  txLink: { flexDirection: 'row', alignItems: 'center', gap: 6 },

  // Timeline
  timelineRow: { flexDirection: 'row', alignItems: 'flex-start' },
  timelineLeft: { width: 28, alignItems: 'center' },
  timelineDot: {
    width: 16, height: 16, borderRadius: 8,
    backgroundColor: '#E0E0E0',
    justifyContent: 'center', alignItems: 'center',
    marginTop: 2,
  },
  timelineDotComplete: { backgroundColor: '#16A34A' },
  timelineDotCurrent: { backgroundColor: GOLD },
  timelineLine: { width: 2, flex: 1, backgroundColor: '#E0E0E0', minHeight: 18, marginTop: 2 },
  timelineLineComplete: { backgroundColor: '#16A34A' },
  timelineRight: { flex: 1, paddingBottom: 14, marginLeft: 8 },
  timelineLabel: { fontSize: 13, color: '#888', fontWeight: '600' },
  timelineLabelCurrent: { color: '#1A1A1A', fontWeight: '700' },
  timelineTime: { fontSize: 11, color: '#AAA', marginTop: 2 },

  proofImage: { width: '100%', height: 200, borderRadius: 12, backgroundColor: '#F0F0F0' },

  actionBar: {
    position: 'absolute',
    bottom: 0, left: 0, right: 0,
    backgroundColor: '#FFF',
    padding: 16,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    gap: 8,
  },
  actionBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
    paddingVertical: 14,
    borderRadius: 14,
  },
  actionBtnText: { color: '#FFF', fontWeight: '700', fontSize: 15 },
  refundActions: { flexDirection: 'row', gap: 10 },
  refundRejectBtn: { flex: 1, backgroundColor: '#FEE2E2' },

  modalOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  modalContent: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 24 },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 8 },
  modalHint: { fontSize: 13, color: '#888', marginBottom: 20, lineHeight: 18 },
  inputLabel: { fontSize: 12, fontWeight: '700', color: '#888', marginBottom: 6, marginTop: 10 },
  input: {
    backgroundColor: '#F9F9F9',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 10,
    fontSize: 14,
    color: '#1A1A1A',
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  modalActions: { flexDirection: 'row', gap: 10, marginTop: 20 },
  modalBtn: { flex: 1, paddingVertical: 14, borderRadius: 12, alignItems: 'center' },
  modalBtnPrimary: { backgroundColor: GOLD },
  modalBtnPrimaryText: { color: '#FFF', fontWeight: '700', fontSize: 15 },
  modalBtnSecondary: { backgroundColor: '#F5F5F5' },
  modalBtnSecondaryText: { color: '#666', fontWeight: '700', fontSize: 15 },

  // Delivery proof picker
  proofPickerRow: { flexDirection: 'row', gap: 10, marginTop: 8 },
  proofPickerBtn: {
    flex: 1, paddingVertical: 20, paddingHorizontal: 12,
    borderRadius: 12, borderWidth: 1.5, borderStyle: 'dashed',
    borderColor: GOLD, backgroundColor: '#FFFBEB',
    alignItems: 'center', gap: 6,
  },
  proofPickerText: { fontSize: 12, color: GOLD, fontWeight: '700', textAlign: 'center' },
  proofPreviewWrap: { marginTop: 8, position: 'relative' },
  proofPreview: { width: '100%', height: 180, borderRadius: 12 },
  removeProofBtn: {
    position: 'absolute', top: 8, right: 8,
    width: 28, height: 28, borderRadius: 14,
    backgroundColor: 'rgba(0,0,0,0.6)',
    justifyContent: 'center', alignItems: 'center',
  },
  uploadingRow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginTop: 12 },

  retryBtn: {
    paddingHorizontal: 20,
    paddingVertical: 10,
    backgroundColor: GOLD,
    borderRadius: 20,
  },
  retryText: { color: '#FFF', fontWeight: '700', fontSize: 14 },
});
